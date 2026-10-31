#!/bin/bash
# Rebuilds the Go `nexal` helper and the Mac app (NexalMac) and swaps them into the
# installed app, keeping its existing nexal-network runtime (the normal packaging
# would replace an ML-KEM-1024 runtime with stock NetBird). Re-signs and verifies.
# Usage: bash macos/scripts/update-helper.sh ["Developer ID Application: …"]
#   APP=/path/to/App.app           the installed app (default: the first /Applications/neXal*.app that has a helper)
#   NEXAL_SKIP_TESTS=1             install even if the Go tests fail (the failing packages are still printed)
#   NEXAL_SKIP_VMNET=1             do not install the LAN bridge helper
#   NEXAL_REFRESH_ICONS=1          clear the icon cache and restart the Dock and Finder even if the icon is unchanged
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
for tool in go swift clang lipo codesign security osascript; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool is not installed or not on PATH; this update needs it"; exit 1; }
done
if [ -z "${APP:-}" ]; then
  for candidate in "/Applications/neXal-Connector.app" /Applications/neXal*.app; do
    if [ -x "$candidate/Contents/Helpers/nexal" ]; then APP=$candidate; break; fi
  done
fi
APP=${APP:-/Applications/neXal-Connector.app}
STAGE=build
# Once the app has been stopped, a failure leaves a half-updated app: say where the old binaries are.
trap 'rc=$?; if [ "$rc" != 0 ]; then echo "update FAILED at line $LINENO during \"$STAGE\" (exit $rc)."; if [ "$STAGE" = install ]; then echo "The app was stopped and may be half-updated. Previous binaries: $OUT (nexal.previous, NexalMac.previous). Re-run this script after fixing the error above."; else echo "Nothing was installed or stopped; the app is as it was."; fi; fi' EXIT
# Developer ID if this Mac has one, else Apple Development, else ad hoc ("-"): a local
# build only needs a valid signature that carries nexal-vmhost's entitlement.
ids=$(security find-identity -v -p codesigning)
IDENTITY=${1:-$(printf '%s\n' "$ids" | sed -n 's/.*"\(Developer ID Application:[^"]*\)".*/\1/p' | head -1)}
[ -n "$IDENTITY" ] || IDENTITY=$(printf '%s\n' "$ids" | sed -n 's/.*"\(Apple Development:[^"]*\)".*/\1/p' | head -1)
[ -n "$IDENTITY" ] || IDENTITY=-
if [ "$IDENTITY" != "-" ] && ! printf '%s\n' "$ids" | grep -qF "\"$IDENTITY\""; then
  echo "no signing identity named \"$IDENTITY\" in this Mac's keychain. Available:"
  printf '%s\n' "$ids" | sed -n 's/.*"\(.*\)".*/  \1/p'
  exit 1
fi
echo "== signing as: $IDENTITY"
[ -x "$APP/Contents/Helpers/nexal" ] || { echo "$APP has no Contents/Helpers/nexal"; exit 1; }
OUT=$(mktemp -d)
STAGE=test
echo "== test"
if ! (cd "$ROOT/connector" && go test ./internal/client/... ./internal/mesh/... ./internal/agent/... ./internal/remoteinference/... ./internal/runpod/...); then
  if [ "${NEXAL_SKIP_TESTS:-}" = 1 ]; then
    echo "warning: tests failed (see above); continuing because NEXAL_SKIP_TESTS=1"
  else
    echo "Tests failed (see above), so nothing was installed. Fix them, or install anyway with: NEXAL_SKIP_TESTS=1 bash $0"
    exit 1
  fi
fi
STAGE=build
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
# The home-network bridge for VMs (installed below, and from Settings in the app).
clang -O2 -Wall -Wextra -Wno-unused-parameter -mmacosx-version-min=14.0 -arch arm64 -arch x86_64 \
  -o "$OUT/nexal-vmnet" "$ROOT/macos/vmnet/nexal-vmnet.c" \
  -framework vmnet -framework SystemConfiguration -framework CoreFoundation
echo "== install (backup: $OUT/nexal.previous)"
STAGE=install
# Remember whether the icon changed, to refresh the icon caches only when it did.
ICON_CHANGED=0
cmp -s "$ROOT/macos/Resources/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns" || ICON_CHANGED=1
cmp -s "$ROOT/macos/Resources/Info.plist" "$APP/Contents/Info.plist" || ICON_CHANGED=1
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
# Wait for the agent to be gone (it holds the configuration lock); force it after 15 s.
for _ in $(seq 1 15); do
  pgrep -f "Helpers/nexal run|$SUPPORT_BIN run" >/dev/null 2>&1 || break
  sleep 1
done
if pgrep -f "Helpers/nexal run|$SUPPORT_BIN run" >/dev/null 2>&1; then
  echo "the connector agent did not stop; forcing it"
  pkill -9 -f "Helpers/nexal run" 2>/dev/null || true
  pkill -9 -f "$SUPPORT_BIN run" 2>/dev/null || true
  sleep 1
fi
cp "$APP/Contents/Helpers/nexal" "$OUT/nexal.previous"
cp "$APP/Contents/MacOS/NexalMac" "$OUT/NexalMac.previous"
sudo install -m 755 "$OUT/nexal" "$APP/Contents/Helpers/nexal"
sudo install -m 755 "$OUT/swift/out/Products/Release/NexalMac" "$APP/Contents/MacOS/NexalMac"
sudo install -m 755 "$VMHOST" "$APP/Contents/Helpers/nexal-vmhost"
sudo install -m 755 "$OUT/nexal-vmnet" "$APP/Contents/Helpers/nexal-vmnet"
sudo install -m 755 "$ROOT/macos/scripts/install-vmnet.sh" "$APP/Contents/Resources/install-vmnet.sh"
# The container-runtime installer behind Settings › Virtual Machine Hosting › Set up (Colima,
# docker, devpod). package-app.sh ships it; this update path must too.
sudo install -m 755 "$ROOT/macos/Resources/install-container-runtime.sh" "$APP/Contents/Resources/install-container-runtime.sh"
sudo install -m 644 "$ROOT/macos/Resources/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"
# The icon is named in Info.plist (CFBundleIconFile): an install made before that key existed keeps a
# blank icon unless the plist is refreshed too. Then ask Launch Services and the Dock to look again.
sudo install -m 644 "$ROOT/macos/Resources/Info.plist" "$APP/Contents/Info.plist"
# Official OS logos (macos/Resources/os-logos/os-*.png), shown on VM and dev container rows.
for logo in "$ROOT"/macos/Resources/os-logos/os-*.png; do
  [ -f "$logo" ] && sudo install -m 644 "$logo" "$APP/Contents/Resources/$(basename "$logo")"
done
# Translations (macos/Resources/*.lproj), so a language added since the last install shows up.
for lproj in "$ROOT"/macos/Resources/*.lproj; do
  [ -d "$lproj" ] && sudo ditto "$lproj" "$APP/Contents/Resources/$(basename "$lproj")"
done
# Stamp the installed app with the commit it was built from (before signing, so the seal covers it).
BUILD_COMMIT=$(cd "$ROOT" && git rev-parse --short HEAD 2>/dev/null || echo unknown)
if [ -n "$(cd "$ROOT" && git status --porcelain --untracked-files=no 2>/dev/null)" ]; then BUILD_COMMIT="$BUILD_COMMIT+local-changes"; fi
printf '%s %s\n' "$BUILD_COMMIT" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" | sudo tee "$APP/Contents/Resources/BUILD_COMMIT" >/dev/null
# Sign as the current user: root cannot reach the login keychain's private key
# (codesign fails with errSecInternalComponent under sudo).
sudo chown -R "$(id -un)" "$APP"
# Secure timestamps are only needed for notarized Developer ID builds and need
# Apple's timestamp server; skip them for local development identities.
codesign --force --options runtime "$TS" --sign "$IDENTITY" "$APP/Contents/Helpers/nexal"
codesign --force --options runtime "$TS" --sign "$IDENTITY" "$APP/Contents/MacOS/NexalMac"
codesign --force --options runtime "$TS" --sign "$IDENTITY" "$APP/Contents/Helpers/nexal-vmnet"
# nexal-vmhost needs the virtualization entitlement, or macOS refuses to start a VM.
codesign --force --options runtime "$TS" --entitlements "$ROOT/macos/NexalVMHost.entitlements" --sign "$IDENTITY" "$APP/Contents/Helpers/nexal-vmhost"
codesign -d --entitlements - "$APP/Contents/Helpers/nexal-vmhost" 2>/dev/null | grep -q com.apple.security.virtualization \
  || { echo "nexal-vmhost is missing the virtualization entitlement"; exit 1; }
codesign --force --options runtime "$TS" --sign "$IDENTITY" "$APP"
codesign --verify --deep --strict "$APP"
touch "$APP"
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP" >/dev/null 2>&1 || true
# Finder and the Dock keep the icon they first saw for this app (blank, before it had one) in an icon cache.
if [ "$ICON_CHANGED" = 1 ] || [ "${NEXAL_REFRESH_ICONS:-}" = 1 ]; then
  sudo rm -rf /Library/Caches/com.apple.iconservices.store 2>/dev/null || true
  killall iconservicesagent iconservicesd 2>/dev/null || true
  killall Dock Finder 2>/dev/null || true
fi
echo "Icon in the installed app: $(/usr/libexec/PlistBuddy -c 'Print :CFBundleIconFile' "$APP/Contents/Info.plist" 2>&1) ($(ls -l "$APP/Contents/Resources/AppIcon.icns" 2>&1 | awk '{print $5}') bytes)"
if [ -f "$SUPPORT_BIN" ]; then
  cp "$SUPPORT_BIN" "$OUT/nexal.support.previous"
  install -m 755 "$APP/Contents/Helpers/nexal" "$SUPPORT_BIN"
  echo "Refreshed $SUPPORT_BIN"
fi
# Symlink the Nexal bin directory onto PATH so `nexal`, `nexal-network` etc. are
# available from the terminal without manual shell-profile edits.
NEXAL_BIN="$HOME/Library/Application Support/Nexal/bin"
if [ -d "$NEXAL_BIN" ]; then
  for bin in "$NEXAL_BIN"/*; do
    [ -x "$bin" ] || continue
    name=$(basename "$bin")
    sudo ln -sf "$bin" "/usr/local/bin/$name" 2>/dev/null \
      && echo "Linked /usr/local/bin/$name" \
      || echo "warning: could not link /usr/local/bin/$name"
  done
fi
# The LAN bridge helper (VMs get a home-network address too). Skip with NEXAL_SKIP_VMNET=1.
if [ "${NEXAL_SKIP_VMNET:-}" != 1 ]; then
  bash "$ROOT/macos/scripts/install-vmnet.sh" --binary "$APP/Contents/Helpers/nexal-vmnet" \
    || echo "warning: nexal-vmnet was not installed; VMs keep NAT networking only"
fi
# The macOS firewall ties each allowance to the binary's signature, so re-signing above voids it: inbound
# connections to model servers and the bandwidth test (nexal) and to the mesh runtime are then dropped
# silently. Allow them again (the app also checks this before it serves a model).
FW=/usr/libexec/ApplicationFirewall/socketfilterfw
for bin in "$APP/Contents/Helpers/nexal" "$APP/Contents/Helpers/nexal-network" "$SUPPORT_BIN"; do
  [ -x "$bin" ] || continue
  sudo "$FW" --add "$bin" >/dev/null 2>&1 || true
  sudo "$FW" --unblockapp "$bin" >/dev/null 2>&1 || true
  echo "Firewall: allowed incoming connections for $bin"
done
STAGE=done
open "$APP"
echo "Installed build: $(cat "$APP/Contents/Resources/BUILD_COMMIT")"
echo "Done. Previous binaries kept in $OUT (nexal.previous, NexalMac.previous) for rollback."
