#!/usr/bin/env bash
# Provision the cloudflared origin that internal/tunnel supervises.
#
# Run this ON THE MAC that should be the always-on origin (the M4 mini). It installs
# a pinned cloudflared, records its provenance, writes the token file with the
# permissions the supervisor demands, and prints the connector config block.
#
#   bash scripts/setup-cloudflared-origin.sh --hostname mini.nexal.systems
#
# WHAT THIS DOES NOT DO. It does not start a tunnel and it does not launch
# cloudflared. internal/tunnel is the only thing that launches it, because the launch
# policy -- QUIC, --post-quantum, a generated config, a token FILE rather than an
# argument, and a stripped environment -- lives in BuildArgs and CleanEnv and must not
# be reimplemented here. A second launch path is a second policy, and the weaker one
# wins. This script only prepares what Validate() insists on.
#
# THE TOKEN IS NEVER PASSED AS AN ARGUMENT OR READ FROM THE ENVIRONMENT. It is typed
# into a silent prompt, or piped in on stdin. An argument is visible in `ps` to every
# process on the machine and lands in shell history.
set -euo pipefail

# The pinned release is NOT restated here. It lives in
# connector/internal/tunnel/pinned.json, which the connector embeds and validates, so
# there is exactly one place to bump and the Go tests cover it.
#
# It used to be duplicated in this script, guarded by a test that read the script. That
# test could never fail: a file outside the Go module root is invisible to the test
# cache, so the result was reused forever, including under CI's plain `go test -race`.
# Reading the pins from the embedded file removes the duplication instead of watching it.
PINS="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/connector/internal/tunnel/pinned.json"
[ -f "$PINS" ] || { echo "Cannot find pinned.json at $PINS -- run this from a checkout."; exit 1; }

# Flat JSON, read with sed so this has no python/jq dependency on a clean macOS.
pin() {
  local v
  v="$(sed -n "s/.*\"$1\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$PINS" | head -1)"
  [ -n "$v" ] || { echo "pinned.json is missing \"$1\"" >&2; exit 1; }
  printf '%s' "$v"
}
VERSION="$(pin version)"
ARCH="$(pin architecture)"
ASSET="$(pin asset)"
SOURCE_URL="$(pin sourceUrl)"
BINARY_SHA256="$(pin sha256)"
VERIFICATION_METHOD="$(pin verificationMethod)"

PREFIX="${PREFIX:-/usr/local/nexal}"
BINARY="${BINARY:-$PREFIX/bin/cloudflared}"
TOKEN_FILE="${TOKEN_FILE:-$PREFIX/etc/cloudflared-token}"
HOSTNAME=""

while [ $# -gt 0 ]; do
  case "$1" in
    --hostname) HOSTNAME="${2:-}"; shift 2 ;;
    *) echo "Unknown argument: $1"; exit 2 ;;
  esac
done

if [ -z "$HOSTNAME" ]; then
  echo "A tunnel hostname is required, and it must be a real name you control:"
  echo "  bash scripts/setup-cloudflared-origin.sh --hostname mini.nexal.systems"
  exit 2
fi
# Mirrors hostnameRE and the dot/'..' checks in Validate() so a bad name fails here,
# with an explanation, rather than inside the agent later.
case "$HOSTNAME" in
  *..*|-*|*-) echo "Invalid hostname: $HOSTNAME"; exit 2 ;;
esac
if ! printf '%s' "$HOSTNAME" | grep -Eq '^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$' \
   || ! printf '%s' "$HOSTNAME" | grep -q '\.'; then
  echo "Hostname must be lowercase, dotted and DNS-shaped: $HOSTNAME"
  exit 2
fi

[ "$(uname -s)" = "Darwin" ] || { echo "This provisions a macOS origin."; exit 1; }
# Validate() compares the pinned architecture against runtime.GOARCH, so an Intel
# Mac would fail there. Failing here says why.
[ "$(uname -m)" = "arm64" ] || { echo "This Mac is $(uname -m); the pin is arm64."; exit 1; }

echo "Installing cloudflared ${VERSION} for ${HOSTNAME}"
sudo mkdir -p "$(dirname "$BINARY")" "$(dirname "$TOKEN_FILE")"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
curl -fsSL --proto '=https' --tlsv1.2 -o "$WORK/$ASSET" "$SOURCE_URL"
tar xzf "$WORK/$ASSET" -C "$WORK"
[ -f "$WORK/cloudflared" ] || { echo "Release layout changed: no cloudflared in $ASSET"; exit 1; }

ACTUAL="$(shasum -a 256 "$WORK/cloudflared" | awk '{print $1}')"
if [ "$ACTUAL" != "$BINARY_SHA256" ]; then
  echo "INTEGRITY CHECK FAILED -- refusing to install."
  echo "  expected $BINARY_SHA256"
  echo "  actual   $ACTUAL"
  echo "Either the pin is stale for a re-tagged release, or this download is not what"
  echo "we verified. Do not proceed by editing the pin without re-verifying."
  exit 1
fi
echo "Digest matches the pin."

# 0755 root-owned: Validate() rejects a binary that is group- or world-writable,
# because anything that can rewrite it can run arbitrary code inside the launch
# policy. Installed atomically so the agent never sees a half-written file.
sudo install -m 0755 -o root -g wheel "$WORK/cloudflared" "$BINARY.new"
sudo mv -f "$BINARY.new" "$BINARY"

echo
if [ -s "$TOKEN_FILE" ]; then
  echo "Token file already present at $TOKEN_FILE, leaving it alone."
else
  echo "Paste the tunnel token from Cloudflare Zero Trust"
  echo "  (Networks > Tunnels > your tunnel > Install and run a connector)."
  echo "It is not echoed, and it is not passed as an argument -- arguments are"
  echo "visible in ps to every process on this Mac."
  if [ -t 0 ]; then
    read -rs -p "Token: " TOKEN; echo
  else
    read -r TOKEN
  fi
  TOKEN="$(printf '%s' "$TOKEN" | tr -d '[:space:]')"
  # Mirrors the supervisor's own token checks so a truncated paste is caught now.
  if [ "${#TOKEN}" -lt 32 ]; then
    echo "That token is ${#TOKEN} characters; the supervisor requires at least 32."
    exit 1
  fi
  # umask first so the secret is never briefly readable, and written via a temp file
  # so a reader never sees a partial token.
  ( umask 077; printf '%s' "$TOKEN" | sudo tee "$TOKEN_FILE.new" >/dev/null )
  sudo chmod 0600 "$TOKEN_FILE.new"
  sudo chown root:wheel "$TOKEN_FILE.new"
  sudo mv -f "$TOKEN_FILE.new" "$TOKEN_FILE"
  unset TOKEN
  echo "Token written to $TOKEN_FILE (0600)."
fi

echo
echo "Installed:"
ls -l "$BINARY" | sed 's/^/  /'
"$BINARY" --version 2>&1 | sed 's/^/  /'
echo
echo "Add this to the connector config as the \"tunnel\" object:"
cat <<JSON
{
  "binary": "$BINARY",
  "sha256": "$BINARY_SHA256",
  "version": "$VERSION",
  "architecture": "$ARCH",
  "sourceUrl": "$SOURCE_URL",
  "verificationMethod": "$VERIFICATION_METHOD",
  "tokenFile": "$TOKEN_FILE",
  "hostname": "$HOSTNAME"
}
JSON
echo
echo "The supervisor launches cloudflared; nothing here does. Two things still gate"
echo "real traffic, and neither is a bug in this script:"
echo "  - the connector's local API must bind loopback, which the tunnel fronts;"
echo "  - agent.go still refuses production dispatch pending verified tunnel"
echo "    integration, so this makes the origin EXIST, not finished."
echo
echo "Put a Cloudflare Access policy in front of $HOSTNAME before relying on it."
echo "A tunnel makes this Mac reachable from the internet; the hostname is not a secret."
