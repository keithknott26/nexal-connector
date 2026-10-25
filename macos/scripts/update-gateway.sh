#!/bin/bash
# Upgrades the storage gateway's mesh runtime to experiments/mlkem1024-mesh/RUNTIME_VERSION, built here from the
# same pinned sources and patches as update-runtime.sh, and installs the mesh watchdog there.
#   bash macos/scripts/update-gateway.sh [user@gateway]        (default ubuntu@mesh.nexal.systems)
#   WATCHDOG_ONLY=1 bash macos/scripts/update-gateway.sh       just install/update the watchdog
# The gateway rolls itself back if no peer comes up protected within 120 s. Keep SSH access that does not
# depend on the mesh (the OVH address) at hand.
set -euo pipefail
GATEWAY=${1:-ubuntu@mesh.nexal.systems}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
PATCHES="$ROOT/experiments/mlkem1024-mesh"
DEPLOY="$ROOT/connector/deploy/gateway"
VERSION="$(tr -d '[:space:]' < "$PATCHES/RUNTIME_VERSION")-linux"
SSH=(ssh -o ConnectTimeout=15)
WORK=$(mktemp -d /tmp/nexal-gateway.XXXXXX)
mkdir "$WORK/upload"
cp "$DEPLOY"/install-gateway.sh "$DEPLOY"/nexal-mesh-watchdog.py "$DEPLOY"/nexal-mesh-watchdog.service \
   "$DEPLOY"/nexal-mesh-watchdog.timer "$WORK/upload/"
ARG=""
if [ -z "${WATCHDOG_ONLY:-}" ]; then
  case "$("${SSH[@]}" "$GATEWAY" uname -m)" in
    x86_64) GOARCH=amd64 ;; aarch64|arm64) GOARCH=arm64 ;; *) echo "unsupported gateway architecture"; exit 1 ;;
  esac
  echo "== gateway runs $("${SSH[@]}" "$GATEWAY" netbird version); building $VERSION for linux/$GOARCH in $WORK"
  for tool in go curl patch python3 shasum; do command -v "$tool" >/dev/null || { echo "Missing $tool"; exit 1; }; done
  manifest() { python3 -c "import json;print(json.load(open('$PATCHES/upstream.json'))$1)"; }
  curl -fsSL "https://codeload.github.com/netbirdio/netbird/tar.gz/refs/tags/$(manifest "['netbird']['version']")" -o "$WORK/netbird.tar.gz"
  [ "$(shasum -a 256 "$WORK/netbird.tar.gz" | cut -d' ' -f1)" = "$(manifest "['netbird']['archiveSha256']")" ] || { echo "NetBird archive checksum mismatch"; exit 1; }
  mkdir "$WORK/pristine" && tar -xzf "$WORK/netbird.tar.gz" -C "$WORK/pristine"
  moddir() { (cd "$WORK" && GOFLAGS= GOWORK=off go mod download -json "$1@$2" | python3 -c 'import json,sys;print(json.load(sys.stdin)["Dir"])'); }
  RP=$(moddir cunicu.li/go-rosenpass "$(manifest "['rosenpass']['version']")")
  WG=$(moddir "$(manifest "['wireguard']['module']")" "$(manifest "['wireguard']['version']")")
  python3 "$PATCHES/prepare.py" --netbird-source "$(ls -d "$WORK"/pristine/netbird-*)" \
    --rosenpass-source "$RP" --wireguard-source "$WG" --output "$WORK/candidate"
  echo "== test"
  (cd "$WORK/candidate/wireguard" && go test ./device -run 'TestNexal(GateRejects|WireGuardSessionGate|KeyTransition|Renewal)' -count=1 -timeout 120s)
  (cd "$WORK/candidate/netbird" && go test -mod=mod ./client/internal/rosenpass -run 'TestNexal|TestHandshake' -count=1 -timeout 600s)
  echo "== build"
  (cd "$WORK/candidate/netbird" && CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH go build -mod=mod -trimpath \
    -ldflags="-s -w -X github.com/netbirdio/netbird/version.version=$VERSION" -o "$WORK/upload/nexal-network" ./client)
  ARG=$VERSION
fi
echo "== copy to $GATEWAY"
"${SSH[@]}" "$GATEWAY" 'rm -rf ~/nexal-gateway-update && mkdir -m 700 ~/nexal-gateway-update'
scp -q "$WORK"/upload/* "$GATEWAY:nexal-gateway-update/"
echo "== install (sudo on the gateway)"
"${SSH[@]}" -t "$GATEWAY" "sudo bash ~/nexal-gateway-update/install-gateway.sh $ARG"
