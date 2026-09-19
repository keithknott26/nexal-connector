#!/bin/bash
# Run on an Apple-silicon Mac with Xcode and Go installed. No privilege elevation.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP="$ROOT/build/Nexal Connector.app"
test "$(uname -s)" = Darwin || { echo "Packaging requires macOS."; exit 1; }
test "$(uname -m)" = arm64 || { echo "This build recipe targets Apple silicon."; exit 1; }
export MACOSX_DEPLOYMENT_TARGET=14.0
if [ -L "$ROOT/build" ] || [ -L "$APP" ] || { [ -e "$APP" ] && [ ! -d "$APP" ]; }; then
  printf 'Refusing an unsafe or non-directory app destination.\n' >&2; exit 1
fi
mkdir -p "$ROOT/build"
LOCK="$ROOT/build/.package.lock"
if ! mkdir "$LOCK" 2>/dev/null; then
  printf 'Packaging is already running or was interrupted. Inspect .package.lock before removing it and retrying.\n' >&2
  exit 1
fi
STAGE=""
cleanup() {
  # Restore the previous complete bundle if publication was interrupted.
  if [ -n "$STAGE" ] && [ -d "$STAGE/previous.app" ] && [ ! -e "$APP" ]; then
    if ! mv "$STAGE/previous.app" "$APP"; then
      printf 'Previous app retained at %s/previous.app; restore it manually.\n' "$STAGE" >&2
      rmdir "$LOCK" 2>/dev/null || true
      return
    fi
  fi
  [ -z "$STAGE" ] || rm -rf "$STAGE"
  rmdir "$LOCK" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
STAGE="$(mktemp -d "$ROOT/build/.nexal-stage.XXXXXX")"
CANDIDATE="$STAGE/Nexal Connector.app"
(cd "$ROOT" && swift build -c release --arch arm64 && swift test --arch arm64)
mkdir -p "$CANDIDATE/Contents/MacOS" "$CANDIDATE/Contents/Helpers" "$CANDIDATE/Contents/Resources"
BIN="$(cd "$ROOT" && swift build -c release --arch arm64 --show-bin-path)"
install -m 755 "$BIN/NexalMac" "$CANDIDATE/Contents/MacOS/NexalMac"
(cd "$ROOT/../connector" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
    go build -trimpath -ldflags="-s -w" -o "$CANDIDATE/Contents/Helpers/nexal" ./cmd/nexal)
install -m 644 "$ROOT/Resources/Info.plist" "$CANDIDATE/Contents/Info.plist"
/usr/bin/plutil -lint "$CANDIDATE/Contents/Info.plist"
test -x "$CANDIDATE/Contents/MacOS/NexalMac"
test -x "$CANDIDATE/Contents/Helpers/nexal"
# Same-filesystem rename only after every build/test/validation succeeded.
# SIGKILL/power loss during the two renames can require manual recovery from
# .nexal-stage.*/previous.app. Do not claim a transactional filesystem install.
if [ -d "$APP" ]; then mv "$APP" "$STAGE/previous.app"; fi
mv "$CANDIDATE" "$APP"
printf 'Unsigned app assembled: %s\nFollow README signing and notarization gates before distribution.\n' "$APP"
