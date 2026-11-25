# Stiker Generator

Pembuat stiker WhatsApp, termasuk **stiker bersuara**, plus pengunduh media dari YouTube, Facebook, Instagram, dan TikTok. Semuanya dikerjakan dari satu halaman web lokal.

- **WhatsApp:** [whatsmeow](https://github.com/tulir/whatsmeow), klien WhatsApp Web multi-device untuk Go.
- **Halaman web:** `main.go` + `index.html`, satu program Go tanpa framework. Halamannya ikut tertanam di dalam biner.
- **Unduh media:** [yt-dlp](https://github.com/yt-dlp/yt-dlp) + ffmpeg.

## Kebutuhan

- Docker (untuk membangun) atau Go 1.27+
- ffmpeg dengan `libwebp`, `libopus`, dan `libmp3lame` (`sudo apt install ffmpeg` di Ubuntu/Debian)
- Akun WhatsApp di HP (untuk scan QR)
- yt-dlp, hanya kalau mau memakai tab Unduh media

## 1. Ambil dari GitHub

```bash
git clone https://github.com/DitaPutraPratama/go_stiker_generator.git
cd go_stiker_generator
```

## 2. Bangun

```bash
./build.sh
```

Skrip ini membangun `./stiker` di dalam Docker, jadi Go tidak perlu terpasang. Kalau Go sudah ada: `go build -o stiker .`

## 3. Pasang yt-dlp (opsional)

```bash
./pasang-ytdlp.sh
```

Mengunduh yt-dlp resmi ke `bin/yt-dlp` dan memeriksa checksum SHA-256-nya dulu. Tanpa ini, tab Unduh media menampilkan peringatan dan tombolnya nonaktif, sedangkan tab Generator tetap jalan. Paket `apt install yt-dlp` biasanya terlalu lama untuk YouTube sekarang.

## 4. Jalankan

```bash
./stiker
```

Buka **http://127.0.0.1:8080**. Alamat lain: `./stiker -alamat 127.0.0.1:9000`.

### Tab Generator stiker

1. **Tautkan WhatsApp.** Pertama kali muncul kode QR. Di HP buka WhatsApp, Perangkat tertaut, Tautkan perangkat, lalu scan. Formulir di bawahnya terkunci sampai status *tersambung*.
   - **Refresh** memeriksa status ulang.
   - **Konek** menyambung (atau menampilkan QR kalau belum pernah login).
   - **Putuskan** memutus sambungan, sesi tetap tersimpan.
   - **Keluar akun** menghapus sesi tersimpan, jadi sambungan berikutnya meminta QR baru.
2. **Pilih jenis stiker** dan isi formulirnya:

| Jenis | Isian | Suara |
|---|---|---|
| Stiker video | video | dari video, atau ganti dengan file suara |
| Stiker gambar | foto | file suara (kosong = stiker biasa) |
| Stiker gambar dengan teks | foto + teks | file suara (kosong = stiker biasa) |
| Stiker teks dengan suara | teks | file suara, wajib |
| Stiker teks | teks | tidak ada |

3. **Kirim ke** nomor `628…`, atau kosongkan untuk mengirim ke chat dengan diri sendiri.

Pada stiker gambar dengan teks, teks bisa **digeser**: seret di pratinjau dengan mouse atau jari, atau fokuskan pratinjau lalu tekan tombol panah (Shift untuk langkah besar). Kotak putus-putus kuning hanya penanda di pratinjau dan tidak ikut ke stiker. Teks boleh keluar dari batas gambar (bagian luar terpotong). Slider *Ukuran teks* memberi ruang untuk menggeser.

Stiker teks memakai ulang tampilan dari [generator_stiker_teks](https://github.com/DitaPutraPratama/generator_stiker_teks): perataan, huruf, tebal, dan sudut membulat.

### Tab Unduh media

Tempel tautan video **publik**, pilih Video MP4 (kualitas maksimal 360p sampai 1080p, atau terbaik) atau Audio MP3. Berkas diunduh lewat browser. Hanya empat platform yang diterima, tautan lain ditolak.

### Batas

- Stiker animasi maksimal 10 detik dan di bawah 500 KB (kualitas turun otomatis sampai muat). Stiker diam di bawah 100 KB.
- Suara stiker maksimal 10 detik.
- Unduh media: maksimal 300 MB dan 10 menit per tautan.
- Unggahan ke halaman: maksimal 64 MB.

## Perintah berguna

```bash
./build.sh                       # bangun ulang setelah mengubah main.go atau index.html
go test ./...                    # butuh Go dan ffmpeg
./stiker -alamat 127.0.0.1:9000  # ganti port
```

## Catatan

- **`session.db` berisi kunci login WhatsApp Anda.** Dibuat otomatis saat pertama jalan. Siapa pun yang memegangnya bisa bertindak sebagai akun Anda. Sudah ada di `.gitignore`, jangan pernah di-commit atau dibagikan. Kalau terlanjur bocor, keluarkan perangkatnya di HP (Perangkat tertaut), lalu hapus berkasnya.
- **Akses lokal saja.** Server hanya menerima permintaan dari `127.0.0.1` atau `localhost`, dan menolak host atau origin lain, supaya halaman web sembarang tidak bisa memerintah program ini lewat browser Anda. Jangan buka ke jaringan.
- **Stiker bersuara belum resmi.** Formatnya ditiru dari hasil bongkar komunitas (field `audioStickerMessage` pada protokol WhatsApp), bukan dokumentasi resmi, dan masih fitur beta di WhatsApp. Penerima perlu versi WhatsApp yang sudah mendukungnya. Formatnya bisa berubah kapan saja.
- **Risiko WhatsApp.** Memakai klien tidak resmi bisa membuat nomor dibatasi atau diblokir. Untuk percobaan, pakai nomor cadangan.
- **Unduh media.** Hanya unduh konten yang Anda berhak pakai. Mengunduh dari beberapa platform bisa melanggar ketentuan layanannya. Instagram dan Facebook sering menolak tanpa login, jadi hanya video publik yang didukung.
- **YouTube** butuh runtime JavaScript untuk yt-dlp. Pasang `deno`, atau pakai Node 22+. Dengan Node 20 unduhan tetap jalan, tapi sebagian format kualitas tinggi bisa hilang.

## Struktur

```
main.go              server web, koneksi WhatsApp, konversi ffmpeg, unduh yt-dlp
main_test.go         tes konversi, format pesan, keamanan server, unduhan
index.html           halaman web (ikut tertanam di biner)
build.sh             bangun ./stiker lewat Docker
pasang-ytdlp.sh      pasang yt-dlp ke bin/ dengan cek checksum
session.db           sesi WhatsApp (dibuat otomatis, tidak ikut git)
bin/                 yt-dlp (tidak ikut git)
```
