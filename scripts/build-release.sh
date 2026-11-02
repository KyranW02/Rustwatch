#!/bin/sh
# Builds dist/RustWatch.exe and dist/RustWatch.exe.sha256 for a GitHub release.
# Bump Version in internal/app/app.go first, then tag the release v<Version>.
set -e
cd "$(dirname "$0")/.."
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H windowsgui" -o dist/RustWatch.exe ./cmd/rustwatch
(cd dist && sha256sum RustWatch.exe > RustWatch.exe.sha256)
cat dist/RustWatch.exe.sha256
