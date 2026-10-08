#!/bin/bash
# Builds and installs nexal-vmnet, the root helper that gives VMs an address on
# this Mac's home network (vmnet bridged mode -- the way socket_vmnet does it for
# Lima), as a launchd daemon serving only the current user.
#
#   bash macos/scripts/install-vmnet.sh                   # bridge whichever of Wi-Fi/Ethernet is active
#   bash macos/scripts/install-vmnet.sh --interface en0   # always bridge en0
#   bash macos/scripts/install-vmnet.sh --uninstall
#
# Runs as you (it uses sudo for the install): the daemon serves your user id only.
# VMs created after this get a second NIC on the LAN; existing ones are unchanged.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
LABEL=systems.nexal.vmnet
BIN=/Library/PrivilegedHelperTools/$LABEL
PLIST=/Library/LaunchDaemons/$LABEL.plist
SOCK=/var/run/nexal-vmnet.sock
LOG=/var/log/nexal-vmnet.log
USER_ID=$(id -u)
[ "$USER_ID" != 0 ] || { echo "Run this as your normal user (it asks for your password when needed)."; exit 1; }

IFACE=""
ACTION=install
while [ $# -gt 0 ]; do
  case "$1" in
    --interface) IFACE=${2:?--interface needs a name}; shift 2 ;;
    --uninstall) ACTION=uninstall; shift ;;
    *) echo "unknown argument: $1"; exit 2 ;;
  esac
done
case "$IFACE" in ''|en[0-9]|en[0-9][0-9]|bridge[0-9]*) ;; *) echo "unexpected interface name: $IFACE"; exit 2 ;; esac

if [ "$ACTION" = uninstall ]; then
  sudo launchctl bootout "system/$LABEL" 2>/dev/null || true
  sudo rm -f "$PLIST" "$BIN" "$SOCK"
  echo "Removed nexal-vmnet. New VMs get NAT networking only."
  exit 0
fi

OUT=$(mktemp -d)
echo "== build nexal-vmnet"
clang -O2 -Wall -Wextra -Wno-unused-parameter -mmacosx-version-min=14.0 -arch arm64 -arch x86_64 \
  -o "$OUT/nexal-vmnet" "$ROOT/macos/vmnet/nexal-vmnet.c" \
  -framework vmnet -framework SystemConfiguration -framework CoreFoundation
IDENTITY=${NEXAL_SIGN_IDENTITY:-$(security find-identity -v -p codesigning 2>/dev/null | sed -n 's/.*"\(Developer ID Application:[^"]*\)".*/\1/p' | head -1)}
case "$IDENTITY" in
  "Developer ID Application:"*) codesign --force --options runtime --timestamp --sign "$IDENTITY" "$OUT/nexal-vmnet" ;;
  "") codesign --force --sign - "$OUT/nexal-vmnet" ;;
  *) codesign --force --options runtime --timestamp=none --sign "$IDENTITY" "$OUT/nexal-vmnet" ;;
esac
"$OUT/nexal-vmnet" --version

ARGS="<string>$BIN</string><string>--uid</string><string>$USER_ID</string><string>--socket</string><string>$SOCK</string>"
[ -n "$IFACE" ] && ARGS="$ARGS<string>--interface</string><string>$IFACE</string>"
cat > "$OUT/$LABEL.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>$LABEL</string>
	<key>ProgramArguments</key><array>$ARGS</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardErrorPath</key><string>$LOG</string>
	<key>StandardOutPath</key><string>$LOG</string>
</dict>
</plist>
PLIST
plutil -lint "$OUT/$LABEL.plist" >/dev/null

echo "== install (root-owned: a root daemon must never run a binary you can overwrite)"
sudo install -d -m 755 -o root -g wheel /Library/PrivilegedHelperTools
sudo launchctl bootout "system/$LABEL" 2>/dev/null || true
sudo install -m 755 -o root -g wheel "$OUT/nexal-vmnet" "$BIN"
sudo install -m 644 -o root -g wheel "$OUT/$LABEL.plist" "$PLIST"
sudo launchctl bootstrap system "$PLIST"
for _ in 1 2 3 4 5 6 7 8 9 10; do [ -S "$SOCK" ] && break; sleep 0.5; done
if [ -S "$SOCK" ]; then
  echo "nexal-vmnet is running ($SOCK). New VMs get a home-network address too."
else
  echo "nexal-vmnet did not start; last log lines:"; sudo tail -n 20 "$LOG" 2>/dev/null || true; exit 1
fi
rm -rf "$OUT"
