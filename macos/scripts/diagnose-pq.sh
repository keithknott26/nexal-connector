#!/bin/bash
# Read-only ML-KEM-1024 mesh check for any Mac with neXal-Connector installed, and
# optionally the storage gateway. Changes nothing.
#
#   bash macos/scripts/diagnose-pq.sh                  # this Mac + ubuntu@mesh.nexal.systems
#   bash macos/scripts/diagnose-pq.sh --local          # this Mac only
#   bash macos/scripts/diagnose-pq.sh user@gateway     # another gateway
#
# Every runtime must end in the same gated.N as experiments/mlkem1024-mesh/RUNTIME_VERSION;
# a peer on another patch level is refused (reason=peer-lacks-profile) and gets no traffic.
set -uo pipefail
GATEWAY=${1:-ubuntu@mesh.nexal.systems}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
EXPECTED=$(tr -d '[:space:]' < "$ROOT/experiments/mlkem1024-mesh/RUNTIME_VERSION" 2>/dev/null || true)

PEERS='import json,sys
d=json.load(sys.stdin)
for p in d.get("peers",{}).get("details",[]):
    print("  %-34s %-10s %-7s profile=%s installed=%s expires=%s reason=%s" % (
        (p.get("fqdn") or "?").split(".")[0], p.get("status"), p.get("connectionType") or "-",
        p.get("quantumProfile") or "-", p.get("quantumKeyInstalledAt") or "-",
        p.get("quantumKeyExpiresAt") or "-", p.get("quantumReason") or "-"))
print("  self quantumResistance=%s permissive=%s" % (d.get("quantumResistance"), d.get("quantumResistancePermissive")))'

check_version() { # $1 = reported version
  local base=${1%-mac}; base=${base%-linux}
  if [ -z "$EXPECTED" ]; then echo "  (no RUNTIME_VERSION in this checkout to compare)"
  elif [ "$base" = "$EXPECTED" ]; then echo "  OK: matches $EXPECTED"
  else echo "  MISMATCH: expected $EXPECTED - upgrade this peer (it will be refused by upgraded peers)"; fi
}

echo "== This Mac ($(scutil --get ComputerName 2>/dev/null || hostname))"
NB=$(ls /Applications/neXal*.app/Contents/Helpers/nexal-network 2>/dev/null | head -1)
if [ -z "$NB" ]; then
  echo "  no neXal-Connector runtime in /Applications"
else
  V=$("$NB" version 2>&1 | head -1)
  echo "  runtime: $NB"
  echo "  version: $V"
  check_version "$V"
  sudo "$NB" status --json 2>/dev/null | python3 -c "$PEERS" || echo "  status unavailable (is the mesh service running?)"
fi

[ "$GATEWAY" = "--local" ] && exit 0
echo "== Gateway ($GATEWAY)"
GV=$(ssh -o ConnectTimeout=10 "$GATEWAY" 'netbird version' 2>/dev/null | head -1)
if [ -z "$GV" ]; then echo "  unreachable over SSH"; exit 0; fi
echo "  version: $GV"
check_version "$GV"
ssh -o ConnectTimeout=10 "$GATEWAY" "systemctl show netbird -p ActiveEnterTimestamp -p NRestarts -p RestartUSec | sed 's/^/  /'; \
  sudo netbird status --json | python3 -c '$PEERS'; \
  echo \"  log: \$(sudo grep -c 'expired' /var/log/netbird/client.log 2>/dev/null || echo 0) expiry lines, \$(sudo grep -c '^panic:' /var/log/netbird/netbird.err 2>/dev/null || echo 0) panics\""
