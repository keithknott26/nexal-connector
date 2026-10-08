#!/bin/bash
# Rebuilds the Go `nexal` helper and the Mac app (NexalMac) and swaps them into the
# installed app, keeping its existing nexal-network runtime (the normal packaging
# would replace an ML-KEM-1024 runtime with stock NetBird). Re-signs and verifies.
# Usage: bash macos/scripts/update-helper.sh ["Developer ID Application: …"]
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
APP=${APP:-/Applications/neXal-Connector.app}
# Developer ID if this Mac has one, else Apple Development, else ad hoc ("-"): a local
# build only needs a valid signature that carries nexal-vmhost's entitlement.
ids=$(security find-identity -v -p codesigning)
IDENTITY=${1:-$(printf '%s\n' "$ids" | sed -n 's/.*"\(Developer ID Application:[^"]*\)".*/\1/p' | head -1)}
[ -n "$IDENTITY" ] || IDENTITY=$(printf '%s\n' "$ids" | sed -n 's/.*"\(Apple Development:[^"]*\)".*/\1/p' | head -1)
[ -n "$IDENTITY" ] || IDENTITY=-
echo "== signing as: $IDENTITY"
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
# The VM helper (Virtualization.framework, Apple Silicon only) for virtual machines.
(cd "$ROOT/macos" && swift build --scratch-path "$OUT/vmhost" -c release --product nexal-vmhost)
VMHOST="$(cd "$ROOT/macos" && swift build --scratch-path "$OUT/vmhost" -c release --product nexal-vmhost --show-bin-path)/nexal-vmhost"
case "$IDENTITY" in "Developer ID Application:"*) TS=--timestamp ;; *) TS=--timestamp=none ;; esac
# Sign nexal-vmhost before it is installed: if signing fails, the installed copy stays the
# old signed one. A linker-signed (unsigned) helper has no virtualization entitlement and
# every VM fails with "The process doesn't have the com.apple.security.virtualization entitlement".
codesign --force --options runtime "$TS" --entitlements "$ROOT/macos/NexalVMHost.entitlements" --sign "$IDENTITY" "$VMHOST"
codesign -d --entitlements - "$VMHOST" 2>/dev/null | grep -q com.apple.security.virtualization \
  || { echo "nexal-vmhost is missing the virtualization entitlement"; exit 1; }
echo "== install (backup: $OUT/nexal.previous)"
# Quit the app and stop its connector agent: a running process keeps the old code
# in memory even after the file on disk is replaced.
osascript -e 'tell application id "systems.nexal.connector" to quit' 2>/dev/null || true
sleep 2
pkill -x NexalMac 2>/dev/null || true
pkill -f "$APP/Contents/Helpers/nexal run" 2>/dev/null || true
# An older install may run the agent from ~/Library/Application Support/Nexal/bin; stop that
# too and refresh that copy, or the old connector code keeps running after this update.
SUPPORT_BIN="$HOME/Library/Application Support/Nexal/bin/nexal"
pkill -f "$SUPPORT_BIN run" 2>/dev/null || true
sleep 2
cp "$APP/Contents/Helpers/nexal" "$OUT/nexal.previous"
cp "$APP/Contents/MacOS/NexalMac" "$OUT/NexalMac.previous"
sudo install -m 755 "$OUT/nexal" "$APP/Contents/Helpers/nexal"
sudo install -m 755 "$OUT/swift/out/Products/Release/NexalMac" "$APP/Contents/MacOS/NexalMac"
sudo install -m 755 "$VMHOST" "$APP/Contents/Helpers/nexal-vmhost"
# The container-runtime installer behind Settings › Virtual Machine Hosting › Set up (Colima,
# docker, devpod). package-app.sh ships it; this update path must too.
sudo install -m 755 "$ROOT/macos/Resources/install-container-runtime.sh" "$APP/Contents/Resources/install-container-runtime.sh"
# Official OS logos (macos/Resources/os-logos/os-*.png), shown on VM and dev container rows.
for logo in "$ROOT"/macos/Resources/os-logos/os-*.png; do
  [ -f "$logo" ] && sudo install -m 644 "$logo" "$APP/Contents/Resources/$(basename "$logo")"
done
# Sign as the current user: root cannot reach the login keychain's private key
# (codesign fails with errSecInternalComponent under sudo).
sudo chown -R "$(id -un)" "$APP"
# Secure timestamps are only needed for notarized Developer ID builds and need
# Apple's timestamp server; skip them for local development identities.
codesign --force --options runtime "$TS" --sign "$IDENTITY" "$APP/Contents/Helpers/nexal"
codesign --force --options runtime "$TS" --sign "$IDENTITY" "$APP/Contents/MacOS/NexalMac"
# nexal-vmhost needs the virtualization entitlement, or macOS refuses to start a VM.
codesign --force --options runtime "$TS" --entitlements "$ROOT/macos/NexalVMHost.entitlements" --sign "$IDENTITY" "$APP/Contents/Helpers/nexal-vmhost"
codesign -d --entitlements - "$APP/Contents/Helpers/nexal-vmhost" 2>/dev/null | grep -q com.apple.security.virtualization \
  || { echo "nexal-vmhost is missing the virtualization entitlement"; exit 1; }
codesign --force --options runtime "$TS" --sign "$IDENTITY" "$APP"
codesign --verify --deep --strict "$APP"
if [ -f "$SUPPORT_BIN" ]; then
  cp "$SUPPORT_BIN" "$OUT/nexal.support.previous"
  install -m 755 "$APP/Contents/Helpers/nexal" "$SUPPORT_BIN"
  echo "Refreshed $SUPPORT_BIN"
fi
# The LAN bridge helper (VMs get a home-network address too). Skip with NEXAL_SKIP_VMNET=1.
if [ "${NEXAL_SKIP_VMNET:-}" != 1 ]; then
  bash "$ROOT/macos/scripts/install-vmnet.sh" || echo "warning: nexal-vmnet was not installed; VMs keep NAT networking only"
fi
open "$APP"
echo "Done. Previous binaries kept in $OUT (nexal.previous, NexalMac.previous) for rollback."
