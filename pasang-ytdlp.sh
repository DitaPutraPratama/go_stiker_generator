#!/bin/sh
# Mengunduh yt-dlp resmi ke bin/yt-dlp (dipakai tab "Unduh media"), lalu memeriksa
# checksum SHA-256 dari halaman rilis yang sama sebelum dipasang.
set -eu
cd "$(dirname "$0")"
mkdir -p bin
base=https://github.com/yt-dlp/yt-dlp/releases/latest/download
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fL "$base/yt-dlp_linux" -o "$tmp/yt-dlp_linux"
curl -fsSL "$base/SHA2-256SUMS" -o "$tmp/SUMS"
(cd "$tmp" && grep ' yt-dlp_linux$' SUMS | sha256sum -c -)
install -m 755 "$tmp/yt-dlp_linux" bin/yt-dlp
echo "Terpasang yt-dlp $(bin/yt-dlp --version)"
