#!/bin/sh
# Membangun ./stiker di dalam Docker, jadi Go tidak perlu terpasang di laptop.
set -eu
cd "$(dirname "$0")"
docker run --rm -u "$(id -u):$(id -g)" \
  -e HOME=/src/.cache -e GOPATH=/src/.cache/go -e GOFLAGS=-modcacherw -e CGO_ENABLED=0 \
  -v "$PWD":/src -w /src golang:1.27-alpine go build -o stiker .
echo "Selesai: ./stiker"
