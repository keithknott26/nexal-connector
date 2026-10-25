#!/bin/bash
# Runs ON the storage gateway, as root, from the folder update-gateway.sh copied over.
#   sudo bash install-gateway.sh [new-runtime-version]
# With a version (and ./nexal-network beside this script): back up the running NetBird binary, install the new
# one, restart, and roll back on its own unless a peer comes up with the ML-KEM profile within 120 s.
# Always: install or update the mesh watchdog (nexal-mesh-watchdog.timer, every 30 s).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
WANT=${1:-}
[ "$(id -u)" = 0 ] || { echo "run with sudo"; exit 1; }

if [ -n "$WANT" ]; then
  BIN=$(systemctl show -p ExecStart --value netbird | sed -n 's/.*path=\([^ ;]*\).*/\1/p')
  BIN=${BIN:-$(command -v netbird)}
  [ -x "$BIN" ] || { echo "cannot find the running netbird binary"; exit 1; }
  BACKUP=/var/lib/nexal-netbird-upgrade/$(date -u +%Y%m%dT%H%M%SZ)
  install -d -m 700 "$BACKUP"
  cp -p "$BIN" "$BACKUP/netbird.previous"
  echo "== $BIN: $("$BIN" version) -> $WANT (backup in $BACKUP)"
  install -m 755 "$HERE/nexal-network" "$BIN.new"
  [ "$("$BIN.new" version)" = "$WANT" ] || { rm -f "$BIN.new"; echo "built version mismatch"; exit 1; }
  mv -f "$BIN.new" "$BIN"
  systemctl restart netbird
  ok=""
  for _ in $(seq 1 24); do
    sleep 5
    if netbird status --json 2>/dev/null | python3 -c 'import json,sys
d=json.load(sys.stdin); ps=(d.get("peers") or {}).get("details") or []
sys.exit(0 if any(p.get("status")=="Connected" and p.get("quantumProfile") for p in ps) else 1)'; then ok=1; break; fi
  done
  if [ -z "$ok" ]; then
    echo "!! no peer came up with the ML-KEM profile in 120 s: rolling back"
    install -m 755 "$BACKUP/netbird.previous" "$BIN"
    systemctl restart netbird
    exit 1
  fi
  echo "== $WANT is running and peers are protected"
  echo "   roll back: sudo install -m 755 $BACKUP/netbird.previous $BIN && sudo systemctl restart netbird"
fi

install -d -m 755 /usr/local/lib/nexal
install -m 755 "$HERE/nexal-mesh-watchdog.py" /usr/local/lib/nexal/nexal-mesh-watchdog.py
install -m 644 "$HERE/nexal-mesh-watchdog.service" /etc/systemd/system/nexal-mesh-watchdog.service
install -m 644 "$HERE/nexal-mesh-watchdog.timer" /etc/systemd/system/nexal-mesh-watchdog.timer
systemctl daemon-reload
systemctl enable --now nexal-mesh-watchdog.timer
echo "== watchdog on: journalctl -u nexal-mesh-watchdog -f   (evidence: /var/log/nexal-mesh-watchdog/)"
