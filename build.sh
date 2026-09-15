#!/usr/bin/env bash
# Cross-compile bdgw for the BirdDog PLAY (aarch64 Linux, Debian 10).
#
# No cgo and therefore no zig: bdgw dlopens nothing — it drives systemctl and
# talks HTTP to MediaMTX and the PLAY's own API. CGO_ENABLED=0 gives a static
# binary that runs on the device's 2019-vintage glibc with no toolchain pinning,
# the same as bdts and bdplay.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

command -v go >/dev/null || { echo "error: go not found" >&2; exit 1; }

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

go vet ./...
go test ./...

# The tab is a plain script with no build step; nothing else would catch a
# syntax error until a browser silently rendered an empty tab on a device.
# Check the RENDERED asset: the port substitution is what turns
# __BDGW_API_PORT__ into something a parser accepts.
if command -v node >/dev/null; then
  RENDERED="$(mktemp -t bdgw-tab).js"
  trap 'rm -f "$RENDERED"' EXIT
  sed 's/__BDGW_API_PORT__/8093/g' web/streaming.js > "$RENDERED"
  node --check "$RENDERED" || { echo "error: the tab script does not parse" >&2; exit 1; }
  echo "tab:     web/streaming.js parses"
else
  echo "note: node not found, skipping the tab syntax check" >&2
fi

mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
  -o dist/bdgw-linux-arm64 .

echo "built:   dist/bdgw-linux-arm64"
echo "version: ${VERSION}"
file dist/bdgw-linux-arm64
echo "size:    $(du -h dist/bdgw-linux-arm64 | cut -f1)"
