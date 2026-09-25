#!/bin/bash
# Build an explicitly local-development DMG. This never claims notarization.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test "$(uname -s)" = Darwin || { echo "DMG creation requires macOS." >&2; exit 1; }
bash "$ROOT/scripts/package-app.sh"
APP="$ROOT/build/Nexal Connector.app"
VERSION="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$ROOT/Resources/Info.plist")"
OUT="$ROOT/build/Nexal-Connector-$VERSION-local-unsigned.dmg"
STAGE="$(mktemp -d "$ROOT/build/.dmg-stage.XXXXXX")"
cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT
/usr/bin/ditto "$APP" "$STAGE/Nexal Connector.app"
ln -s /Applications "$STAGE/Applications"
/usr/bin/hdiutil create -volname "neXal Connector" -srcfolder "$STAGE" -ov -format UDZO "$OUT"
/usr/bin/shasum -a 256 "$OUT" > "$OUT.sha256"
echo "Local unsigned DMG: $OUT"
echo "For distribution, use the signed/notarized release workflow."
