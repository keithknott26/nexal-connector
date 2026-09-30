#!/bin/bash
# Rebuilds the Go `nexal` helper and the Mac app (NexalMac) and swaps them into the
# installed app, keeping its existing nexal-network runtime (the normal packaging
# would replace an ML-KEM-1024 runtime with stock NetBird). Re-signs and verifies.
# Usage: bash macos/scripts/update-helper.sh ["Developer ID Application: …"]
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
APP=${APP:-/Applications/neXal-Connector.app}
IDENTITY=${1:-$(security find-identity -v -p codesigning | sed -n 's/.*"\(Developer ID Application:[^"]*\)".*/\1/p' | head -1)}
[ -n "$IDENTITY" ] || { echo "No Developer ID identity found; pass it as the first argument."; exit 1; }
[ -x "$APP/Contents/Helpers/nexal" ] || { echo "$APP has no Contents/Helpers/nexal"; exit 1; }
OUT=$(mktemp -d)
echo "== test"
(cd "$ROOT/connector" && go test ./internal/client/... ./internal/mesh/... ./internal/agent/...)
echo "== build"
for arch in arm64 amd64; do
  (cd "$ROOT/connector" && CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags='-s -w' -o "$OUT/nexal-$arch" ./cmd/nexal)
done
lipo -create "$OUT/nexal-arm64" "$OUT/nexal-amd64" -output "$OUT/nexal"
(cd "$ROOT/macos" && swift build --scratch-path "$OUT/swift" -c release --arch arm64 --arch x86_64)
echo "== install (backup: $OUT/nexal.previous)"
# Quit the app and stop its connector agent: a running process keeps the old code
# in memory even after the file on disk is replaced.
osascript -e 'tell application id "systems.nexal.connector" to quit' 2>/dev/null || true
sleep 2
pkill -x NexalMac 2>/dev/null || true
pkill -f "$APP/Contents/Helpers/nexal run" 2>/dev/null || true
sleep 2
cp "$APP/Contents/Helpers/nexal" "$OUT/nexal.previous"
cp "$APP/Contents/MacOS/NexalMac" "$OUT/NexalMac.previous"
sudo install -m 755 "$OUT/nexal" "$APP/Contents/Helpers/nexal"
sudo install -m 755 "$OUT/swift/out/Products/Release/NexalMac" "$APP/Contents/MacOS/NexalMac"
sudo codesign --force --options runtime --timestamp --sign "$IDENTITY" "$APP/Contents/Helpers/nexal"
sudo codesign --force --options runtime --timestamp --sign "$IDENTITY" "$APP/Contents/MacOS/NexalMac"
sudo codesign --force --options runtime --timestamp --sign "$IDENTITY" "$APP"
codesign --verify --deep --strict "$APP"
open "$APP"
echo "Done. Previous binaries kept in $OUT (nexal.previous, NexalMac.previous) for rollback."
