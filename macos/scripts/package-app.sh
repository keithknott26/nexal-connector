#!/bin/bash
# Run on an Apple-silicon Mac with Xcode and Go installed. No privilege elevation.
#
# Produces a UNIVERSAL (arm64 + x86_64) bundle, so one DMG runs natively on both
# Apple silicon and Intel. Rosetta is not a substitute: the helper opens raw
# sockets and reads hardware sensors, and translated builds have different
# performance and sensor behaviour than native ones.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP="$ROOT/build/neXal-Connector.app"
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
CANDIDATE="$STAGE/neXal-Connector.app"
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
# Build the pinned, headless mesh runtime into the neXal bundle. Customers must
# never be sent to install a separately branded desktop application. The BSD
# license permits redistribution; its notice is shipped beside the runtime.
MESH_VERSION="${NEXAL_MESH_RUNTIME_VERSION:-v0.79.0}"
MESH_SOURCE="$STAGE/mesh-source.tar.gz"
MESH_SHA256="a74f9c260ef48cdf8332ffd5379114550f8ed3fe5cf8457449af80b1b9f17dc6"
/usr/bin/curl -fL --proto '=https' --proto-redir '=https' \
  -o "$MESH_SOURCE" "https://github.com/netbirdio/netbird/archive/refs/tags/$MESH_VERSION.tar.gz"
printf '%s  %s\n' "$MESH_SHA256" "$MESH_SOURCE" | /usr/bin/shasum -a 256 -c -
mkdir "$STAGE/mesh-source"
/usr/bin/tar -xzf "$MESH_SOURCE" -C "$STAGE/mesh-source" --strip-components=1
for ARCH in arm64 amd64; do
  (cd "$STAGE/mesh-source" && CGO_ENABLED=0 GOOS=darwin GOARCH="$ARCH" \
    go build -trimpath -ldflags="-s -w -X github.com/netbirdio/netbird/version.version=${MESH_VERSION#v} -X main.commit=nexal-embedded -X main.builtBy=nexal" \
      -o "$STAGE/mesh-$ARCH" ./client)
done
/usr/bin/lipo -create "$STAGE/mesh-arm64" "$STAGE/mesh-amd64" \
    -output "$CANDIDATE/Contents/Helpers/nexal-network"
chmod 755 "$CANDIDATE/Contents/Helpers/nexal-network"
/bin/bash "$ROOT/scripts/bundle-yara-x.sh" "$STAGE" "$CANDIDATE"
install -m 644 "$ROOT/Resources/THIRD-PARTY-NOTICES.txt" "$CANDIDATE/Contents/Resources/THIRD-PARTY-NOTICES.txt"
install -m 644 "$ROOT/Resources/Info.plist" "$CANDIDATE/Contents/Info.plist"
/usr/bin/plutil -lint "$CANDIDATE/Contents/Info.plist"
test -x "$CANDIDATE/Contents/MacOS/NexalMac"
test -x "$CANDIDATE/Contents/Helpers/nexal"
test -x "$CANDIDATE/Contents/Helpers/nexal-network"
# Fail loudly if either slice is missing. Without this a silent fallback to a
# single-architecture build would ship an Intel-broken DMG that looks fine on
# the arm64 machine that built it -- exactly the bug this replaces.
for BINARY in "$CANDIDATE/Contents/MacOS/NexalMac" "$CANDIDATE/Contents/Helpers/nexal" "$CANDIDATE/Contents/Helpers/nexal-network" "$CANDIDATE/Contents/Helpers/yr"; do
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
# The public installer carries bootstrap/networking components only. Private
# inference code, model weights and credentials are fetched per connected session.
PRIVATE_PAYLOAD="$(find "$CANDIDATE" -type f \( -name '*.safetensors' -o -name '*.gguf' -o -name '*.py' -o -name '*.key' -o -name '*.pem' -o -name '*.p12' -o -name '*.pfx' -o -name 'cert.b64' -o -name 'runtime-config.json' -o -name 'session.json' -o -name '.env*' \) -print -quit)"
if [ -n "$PRIVATE_PAYLOAD" ]; then
  printf 'Refusing to package private runtime payloads or credential files.\n' >&2
  exit 1
fi
if [ -n "${NEXAL_CODE_SIGN_IDENTITY:-}" ]; then
  /usr/bin/codesign --force --options runtime --timestamp=none --entitlements "$ROOT/Resources/YARA-X.entitlements" --sign "$NEXAL_CODE_SIGN_IDENTITY" "$CANDIDATE/Contents/Helpers/yr"
  for BINARY in "$CANDIDATE/Contents/Helpers/nexal" "$CANDIDATE/Contents/Helpers/nexal-network" "$CANDIDATE/Contents/MacOS/NexalMac"; do
    /usr/bin/codesign --force --options runtime --timestamp=none --sign "$NEXAL_CODE_SIGN_IDENTITY" "$BINARY"
  done
  /usr/bin/codesign --force --options runtime --timestamp=none --sign "$NEXAL_CODE_SIGN_IDENTITY" "$CANDIDATE"
  /usr/bin/codesign --verify --deep --strict --verbose=2 "$CANDIDATE"
fi
# Same-filesystem rename only after every build/test/validation succeeded.
# SIGKILL/power loss during the two renames can require manual recovery from
# .nexal-stage.*/previous.app. Do not claim a transactional filesystem install.
if [ -d "$APP" ]; then mv "$APP" "$STAGE/previous.app"; fi
mv "$CANDIDATE" "$APP"
if [ -n "${NEXAL_CODE_SIGN_IDENTITY:-}" ]; then
  printf 'Locally signed app assembled: %s\nDeveloper ID signing and notarization are still required for distribution.\n' "$APP"
else
  printf 'Unsigned app assembled: %s\nFollow README signing and notarization gates before distribution.\n' "$APP"
fi
