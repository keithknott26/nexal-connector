#!/bin/bash
# Explicitly install the local-development DMG. Existing installs are moved to
# Trash with a timestamp so this operation is recoverable.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test "${1:-}" = "--confirm-local-signed" || {
  echo "Usage: $0 --confirm-local-signed [path-to-dmg]" >&2
  exit 2
}
DMG="${2:-$(find "$ROOT/build" -maxdepth 1 -name 'Nexal-Connector-*-local-signed.dmg' -print | sort | tail -1)}"
test -f "$DMG" || { echo "Build a local DMG first." >&2; exit 1; }
MOUNT="$(mktemp -d /tmp/nexal-dmg.XXXXXX)"
cleanup() { /usr/bin/hdiutil detach "$MOUNT" >/dev/null 2>&1 || true; rmdir "$MOUNT" 2>/dev/null || true; }
trap cleanup EXIT
/usr/bin/hdiutil attach "$DMG" -nobrowse -readonly -mountpoint "$MOUNT"
SOURCE="$MOUNT/neXal-Connector.app"
TARGET="/Applications/neXal-Connector.app"
test -d "$SOURCE" || { echo "DMG does not contain neXal-Connector.app" >&2; exit 1; }
# Reject unsafe candidates before quitting or moving the current installation.
python3 "$ROOT/scripts/verify-runtime.py" app "$SOURCE"
# Earlier builds were named with a space ("neXal Connector.app"); retire those too
# so two copies never compete for the same background service.
# Quit a running copy so its bundle can be replaced.
/usr/bin/osascript -e 'tell application id "systems.nexal.connector" to quit' >/dev/null 2>&1 || true
/usr/bin/pkill -x NexalMac >/dev/null 2>&1 || true
for OLD in "$TARGET" "/Applications/neXal Connector.app" "/Applications/Nexal Connector.app"; do
  if [ -e "$OLD" ]; then
    BACKUP="$HOME/.Trash/$(basename "$OLD" .app)-$(date +%Y%m%d-%H%M%S).app"
    if mv "$OLD" "$BACKUP" 2>/dev/null; then
      echo "Previous installation moved to $BACKUP"
    else
      # Root-owned bundles (e.g. from an earlier elevated install) or macOS
      # App Management protection block a plain mv. Finder's Trash still keeps
      # the old copy recoverable and asks for administrator approval if needed.
      echo "Moving $OLD to Trash via Finder (approve the prompt if asked)…"
      /usr/bin/osascript -e 'on run argv' -e 'tell application "Finder" to delete (POSIX file (item 1 of argv) as alias)' -e 'end run' "$OLD" >/dev/null
      test ! -e "$OLD" || { echo "Could not remove $OLD; drag it to the Trash in Finder, then rerun." >&2; exit 1; }
      echo "Previous installation moved to Trash"
    fi
  fi
done
/usr/bin/ditto "$SOURCE" "$TARGET"
python3 "$ROOT/scripts/verify-runtime.py" app "$TARGET"
echo "Installed $TARGET"
