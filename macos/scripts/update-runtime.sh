#!/bin/bash
# Rebuilds this Mac's mesh runtime (nexal-network) from the same pinned sources and
# ML-KEM-1024 patches as the gateway (including the retry/recovery fixes), swaps it
# into the installed app, re-signs, and restarts the mesh service.
# Usage: bash macos/scripts/update-runtime.sh ["Developer ID Application: …"]
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
PATCHES="$ROOT/experiments/mlkem1024-mesh"
APP=${APP:-/Applications/neXal-Connector.app}
VERSION="0.79.0-nexal-mlkem1024-gated.9-mac"
IDENTITY=${1:-$(security find-identity -v -p codesigning | sed -n 's/.*"\(Developer ID Application:[^"]*\)".*/\1/p' | head -1)}
[ -n "$IDENTITY" ] || { echo "No Developer ID identity found; pass it as the first argument."; exit 1; }
[ -x "$APP/Contents/Helpers/nexal-network" ] || { echo "$APP has no Contents/Helpers/nexal-network"; exit 1; }
for tool in go curl patch python3 shasum lipo codesign; do command -v "$tool" >/dev/null || { echo "Missing $tool"; exit 1; }; done
WORK=$(mktemp -d /tmp/nexal-runtime.XXXXXX)
echo "== working in $WORK"
manifest() { python3 -c "import json;print(json.load(open('$PATCHES/upstream.json'))$1)"; }

echo "== sources"
curl -fsSL "https://codeload.github.com/netbirdio/netbird/tar.gz/refs/tags/$(manifest "['netbird']['version']")" -o "$WORK/netbird.tar.gz"
[ "$(shasum -a 256 "$WORK/netbird.tar.gz" | cut -d' ' -f1)" = "$(manifest "['netbird']['archiveSha256']")" ] || { echo "NetBird archive checksum mismatch"; exit 1; }
mkdir "$WORK/pristine" && tar -xzf "$WORK/netbird.tar.gz" -C "$WORK/pristine"
moddir() { (cd "$WORK" && GOFLAGS= GOWORK=off go mod download -json "$1@$2" | python3 -c 'import json,sys;print(json.load(sys.stdin)["Dir"])'); }
RP=$(moddir cunicu.li/go-rosenpass "$(manifest "['rosenpass']['version']")")
WG=$(moddir "$(manifest "['wireguard']['module']")" "$(manifest "['wireguard']['version']")")

echo "== prepare (verifies source hashes, applies patches)"
python3 "$PATCHES/prepare.py" --netbird-source "$(ls -d "$WORK"/pristine/netbird-*)" \
  --rosenpass-source "$RP" --wireguard-source "$WG" --output "$WORK/candidate"

echo "== test"
(cd "$WORK/candidate/rosenpass" && go test -run 'TestNexal|TestMessages' -count=1 -timeout 180s .)
(cd "$WORK/candidate/netbird" && go test -mod=mod ./client/internal/rosenpass -run 'TestNexal|TestHandshake' -count=1 -timeout 180s)

echo "== build"
for arch in arm64 amd64; do
  (cd "$WORK/candidate/netbird" && CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -mod=mod -trimpath \
    -ldflags="-s -w -X github.com/netbirdio/netbird/version.version=$VERSION" -o "$WORK/nexal-network-$arch" ./client)
done
lipo -create "$WORK/nexal-network-arm64" "$WORK/nexal-network-amd64" -output "$WORK/nexal-network"
[ "$("$WORK/nexal-network" version)" = "$VERSION" ] || { echo "Built version mismatch"; exit 1; }

echo "== install (previous runtime kept at $WORK/nexal-network.previous)"
cp "$APP/Contents/Helpers/nexal-network" "$WORK/nexal-network.previous"
sudo install -m 755 "$WORK/nexal-network" "$APP/Contents/Helpers/nexal-network"
sudo codesign --force --options runtime --timestamp --sign "$IDENTITY" "$APP/Contents/Helpers/nexal-network"
sudo codesign --force --options runtime --timestamp --sign "$IDENTITY" "$APP"
codesign --verify --deep --strict "$APP"
sudo launchctl kickstart -k system/netbird
echo "Installed $VERSION and restarted the mesh service."
echo "Roll back: sudo install -m 755 $WORK/nexal-network.previous \"$APP/Contents/Helpers/nexal-network\" && re-sign && sudo launchctl kickstart -k system/netbird"
echo "Check in ~3 minutes: bash ~/Developer/nexal-platform/self-hosted/netbird/diagnose-pq.sh"
