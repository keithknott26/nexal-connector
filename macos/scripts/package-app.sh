#!/bin/bash
# Run on an Apple-silicon Mac with Xcode and Go installed. No privilege elevation.
#
# Produces a UNIVERSAL (arm64 + x86_64) bundle, so one DMG runs natively on both
# Apple silicon and Intel. Rosetta is not a substitute: the helper opens raw
# sockets and reads hardware sensors, and translated builds have different
# performance and sensor behaviour than native ones.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP="$ROOT/build/Nexal Connector.app"
test "$(uname -s)" = Darwin || { echo "Packaging requires macOS."; exit 1; }
# Build host must be Apple silicon: an arm64 host can cross-compile x86_64, but
# an Intel host cannot produce arm64, so a universal bundle is impossible there.
test "$(uname -m)" = arm64 || { echo "Universal packaging requires an Apple-silicon host."; exit 1; }
export MACOSX_DEPLOYMENT_TARGET=14.0
export CLANG_MODULE_CACHE_PATH="${CLANG_MODULE_CACHE_PATH:-/private/tmp/nexal-connector-clang-cache}"
export SWIFT_MODULECACHE_PATH="${SWIFT_MODULECACHE_PATH:-/private/tmp/nexal-connector-swift-cache}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
export GOCACHE="${GOCACHE:-/private/tmp/nexal-connector-go-cache}"
SWIFT_SCRATCH="${SWIFT_SCRATCH:-/private/tmp/nexal-connector-swift-package}"
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
# Tests run on the host architecture only. A universal test bundle cannot be
# executed for the foreign slice, so testing x86_64 here would require Rosetta
# and would still not prove anything about a real Intel machine.
(cd "$ROOT" && swift test --scratch-path "$SWIFT_SCRATCH" --arch arm64)
(cd "$ROOT" && swift build --scratch-path "$SWIFT_SCRATCH" -c release --arch arm64 --arch x86_64)
mkdir -p "$CANDIDATE/Contents/MacOS" "$CANDIDATE/Contents/Helpers" "$CANDIDATE/Contents/Resources"
BIN="$(cd "$ROOT" && swift build --scratch-path "$SWIFT_SCRATCH" -c release --arch arm64 --arch x86_64 --show-bin-path)"
install -m 755 "$BIN/NexalMac" "$CANDIDATE/Contents/MacOS/NexalMac"
# Go has no universal output mode, so build each slice and join them with lipo.
(cd "$ROOT/../connector" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
    go build -trimpath -ldflags="-s -w" -o "$STAGE/nexal-arm64" ./cmd/nexal)
(cd "$ROOT/../connector" && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o "$STAGE/nexal-amd64" ./cmd/nexal)
/usr/bin/lipo -create "$STAGE/nexal-arm64" "$STAGE/nexal-amd64" \
    -output "$CANDIDATE/Contents/Helpers/nexal"
chmod 755 "$CANDIDATE/Contents/Helpers/nexal"
install -m 644 "$ROOT/Resources/Info.plist" "$CANDIDATE/Contents/Info.plist"
/usr/bin/plutil -lint "$CANDIDATE/Contents/Info.plist"
test -x "$CANDIDATE/Contents/MacOS/NexalMac"
test -x "$CANDIDATE/Contents/Helpers/nexal"
# Fail loudly if either slice is missing. Without this a silent fallback to a
# single-architecture build would ship an Intel-broken DMG that looks fine on
# the arm64 machine that built it -- exactly the bug this replaces.
for BINARY in "$CANDIDATE/Contents/MacOS/NexalMac" "$CANDIDATE/Contents/Helpers/nexal"; do
  ARCHS="$(/usr/bin/lipo -archs "$BINARY")"
  case " $ARCHS " in
    *" arm64 "*) ;;
    *) printf 'Missing arm64 slice in %s (got: %s)\n' "$BINARY" "$ARCHS" >&2; exit 1 ;;
  esac
  case " $ARCHS " in
    *" x86_64 "*) ;;
    *) printf 'Missing x86_64 slice in %s (got: %s)\n' "$BINARY" "$ARCHS" >&2; exit 1 ;;
  esac
done
# Same-filesystem rename only after every build/test/validation succeeded.
# SIGKILL/power loss during the two renames can require manual recovery from
# .nexal-stage.*/previous.app. Do not claim a transactional filesystem install.
if [ -d "$APP" ]; then mv "$APP" "$STAGE/previous.app"; fi
mv "$CANDIDATE" "$APP"
printf 'Unsigned app assembled: %s\nFollow README signing and notarization gates before distribution.\n' "$APP"
