#!/bin/bash
# Explicitly install the local-development DMG. Existing installs are moved to
# Trash with a timestamp so this operation is recoverable.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test "${1:-}" = "--confirm-local-unsigned" || {
  echo "Usage: $0 --confirm-local-unsigned [path-to-dmg]" >&2
  exit 2
}
DMG="${2:-$(find "$ROOT/build" -maxdepth 1 -name 'Nexal-Connector-*-local-unsigned.dmg' -print | sort | tail -1)}"
test -f "$DMG" || { echo "Build a local DMG first." >&2; exit 1; }
MOUNT="$(mktemp -d /tmp/nexal-dmg.XXXXXX)"
cleanup() { /usr/bin/hdiutil detach "$MOUNT" >/dev/null 2>&1 || true; rmdir "$MOUNT" 2>/dev/null || true; }
trap cleanup EXIT
/usr/bin/hdiutil attach "$DMG" -nobrowse -readonly -mountpoint "$MOUNT"
SOURCE="$MOUNT/neXal Connector.app"
TARGET="/Applications/neXal Connector.app"
test -d "$SOURCE" || { echo "DMG does not contain neXal Connector.app" >&2; exit 1; }
if [ -e "$TARGET" ]; then
  BACKUP="$HOME/.Trash/neXal Connector-$(date +%Y%m%d-%H%M%S).app"
  mv "$TARGET" "$BACKUP"
  echo "Previous installation moved to $BACKUP"
fi
/usr/bin/ditto "$SOURCE" "$TARGET"
/usr/bin/codesign --verify --deep --strict "$TARGET" 2>/dev/null || echo "Installed local unsigned build; distribution signature is absent."
echo "Installed $TARGET"
