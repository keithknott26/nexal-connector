#!/bin/bash
# Run on an Apple-silicon Mac with Xcode and Go installed. No privilege elevation.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP="$ROOT/build/Nexal Connector.app"
test "$(uname -s)" = Darwin || { echo "Packaging requires macOS."; exit 1; }
test "$(uname -m)" = arm64 || { echo "This build recipe targets Apple silicon."; exit 1; }
export MACOSX_DEPLOYMENT_TARGET=14.0
(cd "$ROOT" && swift build -c release --arch arm64 && swift test --arch arm64)
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Helpers" "$APP/Contents/Resources"
BIN="$(cd "$ROOT" && swift build -c release --arch arm64 --show-bin-path)"
install -m 755 "$BIN/NexalMac" "$APP/Contents/MacOS/NexalMac"
(cd "$ROOT/../connector" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
    go build -trimpath -ldflags="-s -w" -o "$APP/Contents/Helpers/nexal" ./cmd/nexal)
install -m 644 "$ROOT/Resources/Info.plist" "$APP/Contents/Info.plist"
/usr/bin/plutil -lint "$APP/Contents/Info.plist"
printf 'Unsigned app assembled: %s\nFollow README signing and notarization gates before distribution.\n' "$APP"
