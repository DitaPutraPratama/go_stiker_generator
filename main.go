// Server web lokal: scan QR, lalu kirim stiker WhatsApp (bersuara atau biasa).
package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
	"rsc.io/qr"
)

//go:embed index.html
var halaman []byte

const (
	maxDetik     = "10"
	maxAnimasi   = 500_000
	maxStatis    = 100_000
	mimeAudio    = "audio/ogg; codecs=opus"
	jumlahBatang = 64
)

func ffmpeg(args ...string) ([]byte, error) {
	cmd := exec.Command("ffmpeg", append([]string{"-v", "error", "-nostdin", "-y"}, args...)...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

func gambarStatis(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	}
	return false
}

// buatWebP menurunkan kualitas bertahap sampai muat di batas ukuran stiker.
func buatWebP(input, dir string) (data []byte, animasi bool, err error) {
	const skala = "scale=512:512:force_original_aspect_ratio=decrease,format=rgba,pad=512:512:(ow-iw)/2:(oh-ih)/2:color=black@0"
	out := filepath.Join(dir, "stiker.webp")
	animasi = !gambarStatis(input)
	batas := maxStatis
	if animasi {
		batas = maxAnimasi
	}
	for _, c := range []struct{ fps, q string }{{"15", "70"}, {"12", "50"}, {"10", "35"}, {"8", "20"}, {"6", "10"}, {"5", "1"}} {
		args := []string{"-i", input, "-an"}
		if animasi {
			args = append(args, "-t", maxDetik, "-vf", "fps="+c.fps+","+skala, "-c:v", "libwebp_anim", "-loop", "0")
		} else {
			args = append(args, "-frames:v", "1", "-vf", skala, "-c:v", "libwebp")
		}
		if _, err = ffmpeg(append(args, "-q:v", c.q, out)...); err != nil {
			return nil, animasi, err
		}
		if data, err = os.ReadFile(out); err != nil {
			return nil, animasi, err
		}
		if len(data) <= batas {
			return data, animasi, nil
		}
	}
	return nil, animasi, fmt.Errorf("stiker tetap %d KB (batas %d KB), pakai video yang lebih pendek", len(data)/1000, batas/1000)
}

// buatAudio memakai setelan pesan suara. Menurut implementasi lain, tanpa setelan
// ini stiker tampil tetapi suaranya tidak bisa diputar.
func buatAudio(input, dir string) (data []byte, detik uint32, waveform []byte, err error) {
	out := filepath.Join(dir, "suara.ogg")
	_, err = ffmpeg("-i", input, "-vn", "-map", "0:a:0", "-t", maxDetik, "-ar", "16000", "-ac", "1",
		"-c:a", "libopus", "-b:a", "32k", "-vbr", "on", "-compression_level", "10",
		"-frame_duration", "20", "-application", "voip", "-avoid_negative_ts", "make_zero",
		"-map_metadata", "-1", "-f", "ogg", out)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("tidak ada audio yang bisa dibaca di file ini: %w", err)
	}
	pcm, err := ffmpeg("-i", out, "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1")
	if err != nil {
		return nil, 0, nil, err
	}
	n := len(pcm) / 2
	if n == 0 {
		return nil, 0, nil, errors.New("audio kosong")
	}
	data, err = os.ReadFile(out)
	return data, uint32(math.Ceil(float64(n) / 8000)), hitungWaveform(pcm), err
}

func hitungWaveform(pcm []byte) []byte {
	n := len(pcm) / 2
	rata := make([]float64, jumlahBatang)
	var puncak float64
	for i := range rata {
		a, b := i*n/jumlahBatang, (i+1)*n/jumlahBatang
		for s := a; s < b; s++ {
			rata[i] += math.Abs(float64(int16(binary.LittleEndian.Uint16(pcm[s*2:]))))
		}
		rata[i] /= math.Max(1, float64(b-a))
		puncak = math.Max(puncak, rata[i])
	}
	wf := make([]byte, jumlahBatang)
	if puncak > 0 {
		for i, v := range rata {
			wf[i] = byte(v * 100 / puncak)
		}
	}
	return wf
}

func pesanStiker(st whatsmeow.UploadResponse, animasi bool) *waE2E.StickerMessage {
	now := time.Now()
	return &waE2E.StickerMessage{
		URL: proto.String(st.URL), DirectPath: proto.String(st.DirectPath), MediaKey: st.MediaKey,
		FileSHA256: st.FileSHA256, FileEncSHA256: st.FileEncSHA256, FileLength: proto.Uint64(st.FileLength),
		Mimetype: proto.String("image/webp"), Width: proto.Uint32(512), Height: proto.Uint32(512),
		IsAnimated: proto.Bool(animasi), MediaKeyTimestamp: proto.Int64(now.Unix()), StickerSentTS: proto.Int64(now.UnixMilli()),
	}
}

// susunPesan: Message.134 -> Message.26 (stiker) -> StickerMessage.26 (audio).
func susunPesan(sm *waE2E.StickerMessage, au whatsmeow.UploadResponse, detik uint32, waveform []byte) *waE2E.Message {
	sm.Audio = &waE2E.StickerMessage_AudioMessage{AudioMessage: &waE2E.AudioMessage{
		URL: proto.String(au.URL), DirectPath: proto.String(au.DirectPath), MediaKey: au.MediaKey,
		FileSHA256: au.FileSHA256, FileEncSHA256: au.FileEncSHA256, FileLength: proto.Uint64(au.FileLength),
		Mimetype: proto.String(mimeAudio), Seconds: proto.Uint32(detik), PTT: proto.Bool(true),
		Waveform: waveform, MediaKeyTimestamp: proto.Int64(time.Now().Unix()),
	}}
	return &waE2E.Message{AudioStickerMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{StickerMessage: sm}}}
}

type hasil struct {
	st      []byte
	animasi bool
	au      []byte // nil = stiker biasa tanpa suara
	detik   uint32
	wf      []byte
}

// proses mengubah file unggahan menjadi webp (+ opus kalau ada suara).
// Tanpa file audio, suara diambil dari video; video tanpa suara jadi stiker biasa.
func proses(visual, audio, dir string) (h hasil, err error) {
	eksplisit := audio != ""
	if !eksplisit && !gambarStatis(visual) {
		audio = visual
	}
	if audio != "" {
		au, detik, wf, err := buatAudio(audio, dir)
		switch {
		case err == nil:
			h.au, h.detik, h.wf = au, detik, wf
		case eksplisit:
			return h, err
		}
	}
	h.st, h.animasi, err = buatWebP(visual, dir)
	return h, err
}

type app struct {
	mu    sync.Mutex
	root  context.Context
	db    *sqlstore.Container
	log   waLog.Logger
	cli   *whatsmeow.Client
	batal context.CancelFunc
	state string // menyambung | qr | tersambung | terputus | keluar | galat
	pesan string
	qr    string
	qrN   int
	nomor string
}

// setIf mengubah state hanya bila cli masih klien aktif: peristiwa dari klien lama diabaikan.
func (a *app) setIf(cli *whatsmeow.Client, f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cli == a.cli {
		f()
	}
}

func (a *app) event(cli *whatsmeow.Client, e any) {
	switch e.(type) {
	case *events.Connected:
		a.setIf(cli, func() {
			a.state, a.qr = "tersambung", ""
			if cli.Store.ID != nil {
				a.nomor = cli.Store.ID.User
			}
		})
	case *events.LoggedOut:
		a.setIf(cli, func() { a.state = "keluar" })
	}
}

func (a *app) mulai(ctx context.Context) error {
	a.root, a.log, a.state = ctx, waLog.Stdout("WA", "WARN", true), "terputus"
	db, err := sqlstore.New(ctx, "sqlite", "file:session.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", a.log)
	if err != nil {
		return err
	}
	a.db = db
	return a.konek()
}

// konek membuat klien baru: sudah pernah login -> langsung menyambung, belum -> QR.
func (a *app) konek() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch a.state {
	case "menyambung", "qr", "tersambung":
		return nil
	}
	dev, err := a.db.GetFirstDevice(a.root)
	if err != nil {
		return err
	}
	if a.batal != nil {
		a.batal()
	}
	ctx, batal := context.WithCancel(a.root)
	cli := whatsmeow.NewClient(dev, a.log)
	cli.AddEventHandler(func(e any) { a.event(cli, e) })
	a.cli, a.batal = cli, batal
	a.state, a.pesan, a.qr, a.nomor = "menyambung", "", "", ""
	go func() {
		if err := a.login(ctx, cli); err != nil && ctx.Err() == nil {
			a.setIf(cli, func() { a.state, a.pesan = "galat", err.Error() })
		}
	}()
	return nil
}

func (a *app) login(ctx context.Context, cli *whatsmeow.Client) error {
	if cli.Store.ID != nil {
		return cli.Connect()
	}
	for cli.Store.ID == nil { // QR kedaluwarsa tiap ~2 menit, lalu diulang
		qrc, err := cli.GetQRChannel(ctx)
		if err != nil {
			return err
		}
		if err = cli.Connect(); err != nil {
			return err
		}
		for e := range qrc {
			if e.Event == "code" {
				a.setIf(cli, func() { a.state, a.qr, a.qrN = "qr", e.Code, a.qrN+1 })
			}
		}
		if cli.Store.ID == nil {
			cli.Disconnect()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil // setelah pairing, whatsmeow menyambung ulang sendiri
}

// lepas melepas klien aktif dan menandai terputus; sesi tersimpan tetap ada.
func (a *app) lepas() *whatsmeow.Client {
	a.mu.Lock()
	cli, batal := a.cli, a.batal
	a.cli, a.batal, a.state, a.qr, a.nomor = nil, nil, "terputus", "", ""
	a.mu.Unlock()
	if batal != nil {
		batal()
	}
	return cli
}

func (a *app) putus() {
	if cli := a.lepas(); cli != nil {
		cli.Disconnect()
	}
}

// keluar memutus dan menghapus sesi tersimpan, jadi sambungan berikutnya meminta QR baru.
func (a *app) keluar() {
	ctx, batal := context.WithTimeout(a.root, 10*time.Second)
	defer batal()
	cli := a.lepas()
	if cli == nil {
		if dev, err := a.db.GetFirstDevice(ctx); err == nil && dev.ID != nil {
			dev.Delete(ctx)
		}
		return
	}
	if cli.Store.ID != nil && cli.Logout(ctx) != nil {
		cli.Store.Delete(ctx) // tidak bisa menghubungi server: hapus sesi lokal saja
	}
	cli.Disconnect()
}

// klien mengembalikan klien hanya bila benar-benar tersambung.
func (a *app) klien() (*whatsmeow.Client, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cli, a.state == "tersambung" && a.cli != nil && a.cli.IsConnected()
}

func galat(w http.ResponseWriter, kode int, pesan string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(kode)
	json.NewEncoder(w).Encode(map[string]string{"error": pesan})
}

func (a *app) status(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	state := a.state
	if state == "tersambung" && (a.cli == nil || !a.cli.IsConnected()) {
		state = "menyambung" // koneksi putus sesaat, whatsmeow menyambung ulang sendiri
	}
	s := map[string]any{"state": state, "pesan": a.pesan, "nomor": a.nomor, "n": a.qrN, "ytdlp": ytdlp() != ""}
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s)
}

// aksi membungkus perintah kontrol koneksi (hanya POST) lalu mengembalikan status terbaru.
func (a *app) aksi(f func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			galat(w, 405, "harus POST")
			return
		}
		if err := f(); err != nil {
			galat(w, 500, err.Error())
			return
		}
		a.status(w, r)
	}
}

func (a *app) qrPNG(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	kode := a.qr
	a.mu.Unlock()
	c, err := qr.Encode(kode, qr.L)
	if kode == "" || err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(c.PNG())
}

func ekstensi(nama string) string {
	e := strings.ToLower(filepath.Ext(nama))
	for _, c := range strings.TrimPrefix(e, ".") {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return ""
		}
	}
	if len(e) > 6 {
		return ""
	}
	return e
}

// simpan menyalin file unggahan ke dir; "" bila field tidak dikirim.
func simpan(r *http.Request, field, dir string) (string, error) {
	f, hd, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	p := filepath.Join(dir, field+ekstensi(hd.Filename))
	out, err := os.Create(p)
	if err != nil {
		return "", err
	}
	defer out.Close()
	_, err = io.Copy(out, f)
	return p, err
}

func (a *app) kirim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		galat(w, 405, "harus POST")
		return
	}
	cli, siap := a.klien()
	if !siap {
		galat(w, 409, "WhatsApp belum tersambung, tekan Konek dan scan QR dulu")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		galat(w, 400, "unggahan tidak valid (maks 64 MB): "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	dir, err := os.MkdirTemp("", "stiker")
	if err != nil {
		galat(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(dir)

	vis, err := simpan(r, "visual", dir)
	if err == nil && vis == "" {
		err = errors.New("file belum dipilih")
	}
	var aud string
	if err == nil {
		aud, err = simpan(r, "audio", dir)
	}
	if err != nil {
		galat(w, 400, err.Error())
		return
	}
	h, err := proses(vis, aud, dir)
	if err != nil {
		galat(w, 422, err.Error())
		return
	}
	ctx := r.Context()
	jid, err := tujuan(ctx, cli, strings.TrimSpace(r.FormValue("ke")))
	if err != nil {
		galat(w, 400, err.Error())
		return
	}
	upSt, err := cli.Upload(ctx, h.st, whatsmeow.MediaImage)
	if err != nil {
		galat(w, 502, "unggah stiker: "+err.Error())
		return
	}
	sm := pesanStiker(upSt, h.animasi)
	msg := &waE2E.Message{StickerMessage: sm}
	jenis := "tanpa suara"
	if h.au != nil {
		upAu, err := cli.Upload(ctx, h.au, whatsmeow.MediaAudio)
		if err != nil {
			galat(w, 502, "unggah suara: "+err.Error())
			return
		}
		msg, jenis = susunPesan(sm, upAu, h.detik, h.wf), fmt.Sprintf("dengan suara %d detik", h.detik)
	}
	if _, err = cli.SendMessage(ctx, jid, msg); err != nil {
		galat(w, 502, "kirim: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"pesan": fmt.Sprintf("Terkirim ke %s (%d KB, %s)", jid.User, len(h.st)/1000, jenis)})
}

func tujuan(ctx context.Context, cli *whatsmeow.Client, ke string) (types.JID, error) {
	if ke == "" {
		return cli.Store.ID.ToNonAD(), nil
	}
	if strings.Contains(ke, "@") {
		return types.ParseJID(ke)
	}
	hasil, err := cli.IsOnWhatsApp(ctx, []string{"+" + strings.TrimPrefix(ke, "+")})
	if err != nil {
		return types.JID{}, err
	}
	if len(hasil) == 0 || !hasil[0].IsIn {
		return types.JID{}, fmt.Errorf("%s tidak terdaftar di WhatsApp (pakai format 628...)", ke)
	}
	return hasil[0].JID, nil
}

// ---- unduh media (yt-dlp) ----

// ytdlp mencari yt-dlp di PATH, lalu di bin/ sebelah program. "" bila tidak ada.
// Variabel agar bisa diganti saat tes.
var ytdlp = func() string {
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "bin", "yt-dlp")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Hanya empat platform yang diminta. Daftar putih juga mencegah yt-dlp dipakai
// untuk mengakses alamat lain (jaringan lokal, file://).
var hostUnduh = []string{"youtube.com", "youtu.be", "facebook.com", "fb.watch", "instagram.com", "tiktok.com"}

func urlUnduh(mentah string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(mentah))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", errors.New("tautan tidak valid, harus diawali https://")
	}
	h := strings.ToLower(u.Hostname())
	for _, d := range hostUnduh {
		if h == d || strings.HasSuffix(h, "."+d) {
			return u.String(), nil
		}
	}
	return "", errors.New("hanya tautan YouTube, Facebook, Instagram, dan TikTok yang didukung")
}

func argsUnduh(dir, link, mode string, tinggi int) []string {
	a := []string{"--ignore-config", "--no-playlist", "--no-progress", "--max-filesize", "300M",
		"--windows-filenames", "-P", dir, "-o", "%(title).100B.%(ext)s"}
	if mode == "audio" {
		return append(a, "-f", "bestaudio/best", "-x", "--audio-format", "mp3", "--audio-quality", "0", "--", link)
	}
	f := "bv*+ba/b"
	if tinggi > 0 {
		f = fmt.Sprintf("bv*[height<=%[1]d]+ba/b[height<=%[1]d]/b", tinggi)
	}
	// h264+aac lebih dulu supaya hasil MP4 bisa diputar di mana saja tanpa encode ulang
	return append(a, "-f", f, "-S", "vcodec:h264,acodec:aac", "--merge-output-format", "mp4", "--", link)
}

// jsRuntime: YouTube butuh runtime JavaScript. yt-dlp memakai deno bila ada; selain itu node.
func jsRuntime(bin string) []string {
	if _, err := exec.LookPath("deno"); err == nil {
		return nil
	}
	if _, err := exec.LookPath("node"); err != nil {
		return nil
	}
	if out, _ := exec.Command(bin, "--help").Output(); !bytes.Contains(out, []byte("--js-runtimes")) {
		return nil // yt-dlp lama tidak mengenal opsi ini
	}
	return []string{"--js-runtimes", "node"}
}

func pesanYtdlp(out []byte, err error) string {
	baris := strings.Split(strings.TrimSpace(string(out)), "\n")
	msg := err.Error()
	for i := len(baris) - 1; i >= 0; i-- {
		if strings.Contains(baris[i], "ERROR") {
			msg = baris[i]
			break
		}
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	low := strings.ToLower(msg)
	if strings.Contains(low, "login") || strings.Contains(low, "sign in") || strings.Contains(low, "cookies") {
		msg += " (konten ini butuh login, hanya video publik yang didukung)"
	}
	return msg
}

func (a *app) unduh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		galat(w, 405, "harus POST")
		return
	}
	bin := ytdlp()
	if bin == "" {
		galat(w, 501, "yt-dlp belum terpasang")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := r.ParseForm(); err != nil {
		galat(w, 400, err.Error())
		return
	}
	link, err := urlUnduh(r.FormValue("url"))
	if err != nil {
		galat(w, 400, err.Error())
		return
	}
	mode := r.FormValue("mode")
	if mode != "video" && mode != "audio" {
		galat(w, 400, "format harus video atau audio")
		return
	}
	tinggi := 0
	switch t := r.FormValue("tinggi"); t {
	case "", "0":
	case "1080", "720", "480", "360":
		fmt.Sscan(t, &tinggi)
	default:
		galat(w, 400, "kualitas tidak dikenal")
		return
	}
	dir, err := os.MkdirTemp("", "unduh")
	if err != nil {
		galat(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(dir)

	ctx, batal := context.WithTimeout(r.Context(), 10*time.Minute)
	defer batal()
	args := append(jsRuntime(bin), argsUnduh(dir, link, mode, tinggi)...)
	if out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput(); err != nil {
		galat(w, 422, pesanYtdlp(out, err))
		return
	}
	berkas, err := terbesar(dir)
	if err != nil {
		galat(w, 422, "yt-dlp tidak menghasilkan berkas (mungkin melebihi 300 MB)")
		return
	}
	f, err := os.Open(berkas)
	if err != nil {
		galat(w, 500, err.Error())
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	nama := filepath.Base(berkas)
	w.Header().Set("X-Nama-File", url.PathEscape(nama))
	http.ServeContent(w, r, nama, st.ModTime(), f)
}

// terbesar mengembalikan berkas terbesar di dir, mengabaikan sisa unduhan parsial.
func terbesar(dir string) (string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var best string
	var size int64 = -1
	for _, e := range ents {
		i, err := e.Info()
		if err != nil || e.IsDir() || strings.HasSuffix(e.Name(), ".part") || strings.HasSuffix(e.Name(), ".ytdl") {
			continue
		}
		if i.Size() > size {
			best, size = filepath.Join(dir, e.Name()), i.Size()
		}
	}
	if best == "" {
		return "", errors.New("kosong")
	}
	return best, nil
}

// lokal menolak permintaan dari situs lain: halaman web berbahaya bisa saja memanggil
// 127.0.0.1 dari browser Anda untuk mengirim pesan lewat akun ini.
func lokal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
			http.Error(w, "host ditolak", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host {
				http.Error(w, "origin ditolak", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(halaman)
	})
	mux.HandleFunc("GET /status", a.status)
	mux.HandleFunc("GET /qr.png", a.qrPNG)
	mux.HandleFunc("/konek", a.aksi(a.konek))
	mux.HandleFunc("/putus", a.aksi(func() error { a.putus(); return nil }))
	mux.HandleFunc("/keluar", a.aksi(func() error { a.keluar(); return nil }))
	mux.HandleFunc("/kirim", a.kirim)
	mux.HandleFunc("/unduh", a.unduh)
	return lokal(mux)
}

func main() {
	alamat := flag.String("alamat", "127.0.0.1:8080", "alamat server web")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	syscall.Umask(0o077) // session.db berisi kunci login

	a := &app{}
	if err := a.mulai(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Gagal:", err)
		os.Exit(1)
	}
	srv := &http.Server{Addr: *alamat, Handler: a.routes()}
	go func() {
		<-ctx.Done()
		a.putus()
		srv.Shutdown(context.Background())
	}()
	fmt.Println("Buka di browser: http://" + *alamat)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "Gagal:", err)
		os.Exit(1)
	}
}
