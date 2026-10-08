#!/bin/bash
# Installs nexal-vmnet, the root helper that gives VMs an address on this Mac's
# home network (vmnet bridged mode -- the way socket_vmnet does it for Lima), as
# a launchd daemon serving one user.
#
# From a checkout, as your user (builds it, asks for your password via sudo):
#   bash macos/scripts/install-vmnet.sh [--interface en0]
#   bash macos/scripts/install-vmnet.sh --uninstall
# From the app (Settings › Virtual Machine Hosting › Home network), as root via
# the macOS administrator prompt, with the binary the app ships:
#   install-vmnet.sh --binary <app>/Contents/Helpers/nexal-vmnet --uid <uid>
#
# VMs created after this get a second NIC on the LAN; existing ones are unchanged.
set -euo pipefail
LABEL=systems.nexal.vmnet
BIN=/Library/PrivilegedHelperTools/$LABEL
PLIST=/Library/LaunchDaemons/$LABEL.plist
SOCK=/var/run/nexal-vmnet.sock
LOG=/var/log/nexal-vmnet.log

IFACE=""
ACTION=install
BINARY=""
SERVE_UID=""
while [ $# -gt 0 ]; do
  case "$1" in
    --interface) IFACE=${2:?--interface needs a name}; shift 2 ;;
    --binary) BINARY=${2:?--binary needs a path}; shift 2 ;;
    --uid) SERVE_UID=${2:?--uid needs a number}; shift 2 ;;
    --uninstall) ACTION=uninstall; shift ;;
    *) echo "unknown argument: $1"; exit 2 ;;
  esac
done
case "$IFACE" in ''|en[0-9]|en[0-9][0-9]) ;; *) echo "unexpected interface name: $IFACE"; exit 2 ;; esac

if [ "$(id -u)" = 0 ]; then
  SUDO=""
  if [ "$ACTION" = install ]; then
    case "$SERVE_UID" in ''|*[!0-9]*|0) echo "run as root, --uid <your user id> is required"; exit 2 ;; esac
  fi
else
  SUDO=sudo
  [ -z "$SERVE_UID" ] || [ "$SERVE_UID" = "$(id -u)" ] || { echo "--uid can only name another user when run as root"; exit 2; }
  SERVE_UID=$(id -u)
fi

if [ "$ACTION" = uninstall ]; then
  $SUDO launchctl bootout "system/$LABEL" 2>/dev/null || true
  $SUDO rm -f "$PLIST" "$BIN" "$SOCK"
  echo "Removed the home-network bridge. New VMs get NAT networking only."
  exit 0
fi

OUT=$(mktemp -d)
trap 'rm -rf "$OUT"' EXIT
if [ -n "$BINARY" ]; then
  [ -f "$BINARY" ] && [ -x "$BINARY" ] || { echo "no helper binary at $BINARY"; exit 1; }
  cp "$BINARY" "$OUT/nexal-vmnet"
else
  [ -n "$SUDO" ] || { echo "run as root, --binary is required (no build as root)"; exit 2; }
  ROOT=$(cd "$(dirname "$0")/../.." && pwd)
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
fi
"$OUT/nexal-vmnet" --version

ARGS="<string>$BIN</string><string>--uid</string><string>$SERVE_UID</string><string>--socket</string><string>$SOCK</string>"
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

# Root-owned copy outside the app bundle: a root daemon must never run a binary
# the user (or anything running as the user) can overwrite.
$SUDO install -d -m 755 -o root -g wheel /Library/PrivilegedHelperTools
$SUDO launchctl bootout "system/$LABEL" 2>/dev/null || true
$SUDO install -m 755 -o root -g wheel "$OUT/nexal-vmnet" "$BIN"
$SUDO install -m 644 -o root -g wheel "$OUT/$LABEL.plist" "$PLIST"
$SUDO launchctl bootstrap system "$PLIST"
for _ in 1 2 3 4 5 6 7 8 9 10; do [ -S "$SOCK" ] && break; sleep 0.5; done
if [ -S "$SOCK" ]; then
  echo "Home-network bridge running. New VMs get an address on your home network too."
else
  $SUDO tail -n 20 "$LOG" 2>/dev/null || true
  echo "The home-network bridge did not start (see $LOG)."
  exit 1
fi
