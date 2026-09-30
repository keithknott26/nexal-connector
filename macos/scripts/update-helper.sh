#!/bin/bash
# Rebuilds only the Go `nexal` helper and swaps it into the installed app, keeping
# the app's existing nexal-network runtime. Re-signs the helper and the app.
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
echo "== install (backup: $OUT/nexal.previous)"
osascript -e 'quit app "neXal@home"' 2>/dev/null || true
osascript -e 'quit app "neXal-Connector"' 2>/dev/null || true
sleep 2
cp "$APP/Contents/Helpers/nexal" "$OUT/nexal.previous"
sudo install -m 755 "$OUT/nexal" "$APP/Contents/Helpers/nexal"
sudo codesign --force --options runtime --timestamp --sign "$IDENTITY" "$APP/Contents/Helpers/nexal"
sudo codesign --force --options runtime --timestamp --sign "$IDENTITY" "$APP"
codesign --verify --deep --strict "$APP"
open "$APP"
echo "Done. Roll back with: sudo install -m 755 $OUT/nexal.previous $APP/Contents/Helpers/nexal (then re-run the two codesign lines)"
