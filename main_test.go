package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func ambil(t *testing.T, b []byte, field protowire.Number) []byte {
	t.Helper()
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			t.Fatal("tag rusak")
		}
		b = b[n:]
		if num == field && typ == protowire.BytesType {
			v, _ := protowire.ConsumeBytes(b)
			return v
		}
		b = b[protowire.ConsumeFieldValue(num, typ, b):]
	}
	t.Fatalf("field %d tidak ada", field)
	return nil
}

func TestFormatPesan(t *testing.T) {
	raw, err := proto.Marshal(susunPesan(pesanStiker(whatsmeow.UploadResponse{URL: "s"}, true), whatsmeow.UploadResponse{URL: "a"}, 3, make([]byte, 64)))
	if err != nil {
		t.Fatal(err)
	}
	audio := raw
	for _, f := range []protowire.Number{134, 1, 26, 26} {
		audio = ambil(t, audio, f)
	}
	if got := string(ambil(t, audio, 2)); got != mimeAudio {
		t.Fatal(got)
	}
}

func TestKonversi(t *testing.T) {
	dir := t.TempDir()
	buat := func(nama string, args ...string) string {
		p := filepath.Join(dir, nama)
		if _, err := ffmpeg(append(args, p)...); err != nil {
			t.Fatal(err)
		}
		return p
	}
	suara := []string{"-f", "lavfi", "-i", "sine=d=12"}
	video := append([]string{"-f", "lavfi", "-i", "testsrc2=d=12:s=640x360"}, suara...)
	vid := buat("v.mp4", append(video, "-c:a", "aac")...)
	bisu := buat("bisu.mp4", "-f", "lavfi", "-i", "testsrc2=d=1:s=320x320")
	img := buat("g.png", "-f", "lavfi", "-i", "testsrc2=s=300x500", "-frames:v", "1")

	st, anim, err := buatWebP(vid, dir)
	if err != nil || !anim || len(st) > maxAnimasi || !bytes.Contains(st, []byte("ANMF")) {
		t.Fatal(len(st), anim, err)
	}
	st, anim, err = buatWebP(img, dir)
	if err != nil || anim || len(st) > maxStatis || string(st[8:12]) != "WEBP" {
		t.Fatal(len(st), anim, err)
	}
	au, detik, wf, err := buatAudio(vid, dir)
	if err != nil || string(au[:4]) != "OggS" || detik != 10 || len(wf) != 64 {
		t.Fatal(detik, len(wf), err)
	}
	if _, _, _, err = buatAudio(bisu, dir); err == nil {
		t.Fatal("video bisu harus ditolak")
	}
}

func TestProses(t *testing.T) {
	dir := t.TempDir()
	buat := func(nama string, args ...string) string {
		p := filepath.Join(dir, nama)
		if _, err := ffmpeg(append(args, p)...); err != nil {
			t.Fatal(err)
		}
		return p
	}
	suara := buat("s.mp3", "-f", "lavfi", "-i", "sine=d=2")
	vid := buat("v.mp4", "-f", "lavfi", "-i", "testsrc2=d=2:s=320x320", "-f", "lavfi", "-i", "sine=d=2", "-c:a", "aac")
	bisu := buat("b.mp4", "-f", "lavfi", "-i", "testsrc2=d=1:s=320x320")
	img := buat("g.png", "-f", "lavfi", "-i", "testsrc2=s=300x300", "-frames:v", "1")
	rusak := filepath.Join(dir, "rusak.mp3")
	if _, err := ffmpeg("-f", "lavfi", "-i", "testsrc2=d=1", rusak+".mp4"); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		nama, vis, aud string
		bersuara, anim bool
	}{
		{"video bersuara", vid, "", true, true},
		{"video bisu", bisu, "", false, true},
		{"video + suara lain", bisu, suara, true, true},
		{"gambar tanpa suara", img, "", false, false},
		{"gambar + suara", img, suara, true, false},
	} {
		h, err := proses(c.vis, c.aud, t.TempDir())
		if err != nil || (h.au != nil) != c.bersuara || h.animasi != c.anim || len(h.st) == 0 {
			t.Fatalf("%s: au=%v anim=%v err=%v", c.nama, h.au != nil, h.animasi, err)
		}
	}
	if _, err := proses(img, rusak+".mp4", t.TempDir()); err == nil {
		t.Fatal("audio pilihan sendiri yang tak punya suara harus error")
	}
}

func TestServer(t *testing.T) {
	a := &app{state: "terputus"}
	h := a.routes()
	ytdlp = func() string { return "" }
	do := func(method, path, host, origin string) int {
		r := httptest.NewRequest(method, path, nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for _, c := range []struct {
		nama, method, path, host, origin string
		want                             int
	}{
		{"halaman", "GET", "/", "127.0.0.1:8080", "", http.StatusOK},
		{"status", "GET", "/status", "localhost:8080", "", http.StatusOK},
		{"qr kosong", "GET", "/qr.png", "127.0.0.1:8080", "", http.StatusNotFound},
		{"host asing (DNS rebinding)", "GET", "/", "evil.com", "", http.StatusForbidden},
		{"origin asing (CSRF)", "POST", "/kirim", "127.0.0.1:8080", "http://evil.com", http.StatusForbidden},
		{"belum tersambung", "POST", "/kirim", "127.0.0.1:8080", "http://127.0.0.1:8080", http.StatusConflict},
		{"konek harus POST", "GET", "/konek", "127.0.0.1:8080", "", http.StatusMethodNotAllowed},
		{"putus saat sudah terputus", "POST", "/putus", "127.0.0.1:8080", "http://127.0.0.1:8080", http.StatusOK},
		{"unduh tanpa yt-dlp", "POST", "/unduh", "127.0.0.1:8080", "http://127.0.0.1:8080", http.StatusNotImplemented},
	} {
		if got := do(c.method, c.path, c.host, c.origin); got != c.want {
			t.Errorf("%s: %d, mau %d", c.nama, got, c.want)
		}
	}
}

func TestUrlUnduh(t *testing.T) {
	for _, c := range []struct {
		url string
		ok  bool
	}{
		{"https://www.youtube.com/watch?v=abc", true},
		{"https://youtu.be/abc", true},
		{"https://m.facebook.com/watch/?v=1", true},
		{"https://fb.watch/abc/", true},
		{"https://www.instagram.com/reel/abc/", true},
		{"https://vm.tiktok.com/abc/", true},
		{"https://evilyoutube.com/x", false},
		{"https://youtube.com.evil.com/x", false},
		{"http://127.0.0.1:8080/status", false},
		{"file:///etc/passwd", false},
		{"youtube.com/watch?v=abc", false},
		{"-o /tmp/x", false},
		{"", false},
	} {
		if _, err := urlUnduh(c.url); (err == nil) != c.ok {
			t.Errorf("%q: err=%v, ok seharusnya %v", c.url, err, c.ok)
		}
	}
}

// yt-dlp palsu: mencatat argumen, menulis satu berkas ke dir dari -P.
const ytdlpPalsu = `#!/bin/sh
echo "$@" > "$ARGS_LOG"
for a in "$@"; do
  [ "$prev" = "-P" ] && dir="$a"
  case "$a" in *gagal*) echo "ERROR: Sign in to confirm you are not a bot" >&2; exit 1;; esac
  prev="$a"
done
printf isi > "$dir/Judul Video.mp4"
`

func TestUnduh(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(bin, []byte(ytdlpPalsu), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "args")
	t.Setenv("ARGS_LOG", log)
	ytdlp = func() string { return bin }
	h := (&app{state: "terputus"}).routes()
	post := func(f url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/unduh", strings.NewReader(f.Encode()))
		r.Host = "127.0.0.1:8080"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	w := post(url.Values{"url": {"https://youtu.be/abc"}, "mode": {"video"}, "tinggi": {"720"}})
	if w.Code != 200 || w.Body.String() != "isi" {
		t.Fatalf("video: %d %q", w.Code, w.Body.String())
	}
	if n, _ := url.PathUnescape(w.Header().Get("X-Nama-File")); n != "Judul Video.mp4" {
		t.Fatal("nama berkas:", n)
	}
	args, _ := os.ReadFile(log)
	for _, want := range []string{"height<=720", "--merge-output-format mp4", "--no-playlist", "-- https://youtu.be/abc"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("argumen video tanpa %q: %s", want, args)
		}
	}

	post(url.Values{"url": {"https://youtu.be/abc"}, "mode": {"audio"}})
	args, _ = os.ReadFile(log)
	if !strings.Contains(string(args), "--audio-format mp3") || strings.Contains(string(args), "merge-output") {
		t.Error("argumen audio:", string(args))
	}

	for nama, f := range map[string]url.Values{
		"situs lain":      {"url": {"https://example.com/v"}, "mode": {"video"}},
		"mode ngawur":     {"url": {"https://youtu.be/abc"}, "mode": {"x"}},
		"kualitas ngawur": {"url": {"https://youtu.be/abc"}, "mode": {"video"}, "tinggi": {"9; rm"}},
	} {
		if c := post(f).Code; c != 400 {
			t.Errorf("%s: %d, mau 400", nama, c)
		}
	}
	w = post(url.Values{"url": {"https://youtu.be/gagal"}, "mode": {"video"}})
	if w.Code != 422 || !strings.Contains(w.Body.String(), "butuh login") {
		t.Errorf("galat yt-dlp: %d %s", w.Code, w.Body.String())
	}
}
