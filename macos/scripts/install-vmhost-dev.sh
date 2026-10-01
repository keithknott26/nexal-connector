#!/usr/bin/env bash
# Dev install of nexal-vmhost for this Mac: build, sign with the virtualization entitlement, copy to the
# per-user location the connector checks. Apple Silicon only.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
bin="$("$here/build-vmhost.sh" | tail -1)"
dest="$HOME/Library/Application Support/Nexal/bin"
mkdir -p "$dest"
install -m 755 "$bin" "$dest/nexal-vmhost"
codesign -d --entitlements :- "$dest/nexal-vmhost" 2>/dev/null | grep -q virtualization || { echo "virtualization entitlement missing" >&2; exit 1; }
echo "installed $dest/nexal-vmhost"
