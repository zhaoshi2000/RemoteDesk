#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
go test -count=1 ./...
go vet ./...
mkdir -p dist/linux-amd64 dist/windows-amd64
for command in remote-server remote-agent; do
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w -buildid=' -o "dist/linux-amd64/$command" "./cmd/$command"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags='-s -w -buildid=' -o "dist/windows-amd64/$command.exe" "./cmd/$command"
done
cmake -S native -B build/native -G Ninja -DCMAKE_BUILD_TYPE=Release
cmake --build build/native
ctest --test-dir build/native --output-on-failure
printf '\nBuilt command-line agent/server only; no Qt GUI or finished desktop product.\n'
