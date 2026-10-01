#!/bin/bash
# Pulls the dev-container mesh sidecar image and checks that it carries the patched
# neXal mesh runtime: the NetBird version string and the ML-KEM-1024 profile.
# Usage: bash macos/scripts/verify-sidecar.sh [image] [platform]
#   image    default: DefaultMeshImage from connector/internal/sandbox/devpod.go
#   platform default: the host's (e.g. linux/arm64); pass linux/amd64 to check the other one
# Needs a running docker (Colima/OrbStack/Lima/Podman). Exit 0 only if every check passes.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
IMAGE=${1:-$(sed -n 's/^const DefaultMeshImage = "\(.*\)"$/\1/p' "$ROOT/connector/internal/sandbox/devpod.go")}
PLATFORM=${2:-}
[ -n "$IMAGE" ] || { echo "no image given and DefaultMeshImage not found"; exit 2; }
command -v docker >/dev/null || { echo "docker not found; start Colima/OrbStack/Lima/Podman"; exit 2; }
PROFILE=$(python3 -c "import json;print(json.load(open('$ROOT/macos/scripts/runtime-policy.json'))['profile'])")
# Expected NetBird version: the Dockerfile's RUNTIME_VERSION (kept in step with update-runtime.sh).
WANT=$(sed -n 's/^ARG RUNTIME_VERSION=//p' "$ROOT/connector/sidecar/Dockerfile" | head -1)
MAC=$(sed -n 's/^VERSION="\(.*\)-mac"$/\1/p' "$ROOT/macos/scripts/update-runtime.sh")
plat=(); [ -z "$PLATFORM" ] || plat=(--platform "$PLATFORM")
fail=0; ok() { echo "ok    $*"; }; bad() { echo "FAIL  $*"; fail=1; }
run() { docker run --rm "${plat[@]}" --entrypoint "$1" "$IMAGE" "${@:2}"; }

echo "== $IMAGE ${PLATFORM:+($PLATFORM)}"
docker pull "${plat[@]}" "$IMAGE" >/dev/null && ok "pulled (anonymous pull works only if the ghcr package is public)" || { bad "pull failed (package private? not pushed yet?)"; exit 1; }

[ "${WANT%-linux}" = "$MAC" ] && ok "Dockerfile version matches update-runtime.sh ($MAC)" || bad "Dockerfile RUNTIME_VERSION '$WANT' vs update-runtime.sh '$MAC-*' differ"
got=$(run netbird version | tr -d '\r\n')
[ "$got" = "$WANT" ] && ok "netbird version $got" || bad "netbird version '$got', want '$WANT'"
[ "$(run grep -c -a "$PROFILE" /usr/local/bin/netbird)" -ge 1 ] && ok "ML-KEM profile string $PROFILE present in the binary" || bad "profile $PROFILE not found in the binary"
run cat /etc/nexal-sidecar-release | sed 's/^/      /'
run test -x /usr/local/bin/nexal-sidecar-entrypoint && ok "entrypoint present" || bad "entrypoint missing"
run sh -c 'command -v ip && command -v iptables' >/dev/null && ok "ip, iptables present" || bad "runtime tools missing"
run sh -c '! command -v sshd' >/dev/null && ok "no sshd in the sidecar (SSH is served in the dev container)" || bad "sshd still present in the sidecar"
# Contract smoke test: with no key the entrypoint must report a first-boot failure line, not hang.
out=$(docker run --rm "${plat[@]}" -e NEXAL_HOSTNAME=verify -e NEXAL_MANAGEMENT_URL=https://invalid.example "$IMAGE" 2>&1 || true)
echo "$out" | grep -q '^NEXAL-FIRSTBOOT-FAILED no setup key' && ok "entrypoint reports NEXAL-FIRSTBOOT-FAILED when misconfigured" || bad "unexpected entrypoint output: $out"
[ "$fail" = 0 ] && echo "PASS" || { echo "FAILED"; exit 1; }
