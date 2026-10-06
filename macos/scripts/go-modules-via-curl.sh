#!/bin/bash
# Fill a local Go module proxy with curl, for a Mac where `go` cannot reach proxy.golang.org
# (e.g. broken IPv4 to Google while curl falls back to IPv6). Every module in connector/go.sum
# is fetched; Go still verifies each one against go.sum when it uses them.
# Usage: bash macos/scripts/go-modules-via-curl.sh   then
#        GOPROXY=file://$HOME/.nexal-goproxy GOSUMDB=off bash macos/scripts/update-helper.sh "<identity>"
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
DEST="${DEST:-$HOME/.nexal-goproxy}"
UPSTREAM="${UPSTREAM:-https://proxy.golang.org}"
# Module paths are case-encoded in proxy URLs: an upper-case letter becomes ! plus lower case.
esc() { printf '%s' "$1" | sed -E 's/([A-Z])/!\1/g' | tr 'A-Z' 'a-z'; }
fetched=0 failed=0
while read -r mod ver _; do
  ver=${ver%/go.mod}
  e=$(esc "$mod"); dir="$DEST/$e/@v"; mkdir -p "$dir"
  for ext in mod info zip; do
    f="$dir/$ver.$ext"
    [ -s "$f" ] && continue
    if curl -fsSL --retry 2 -m 120 -o "$f.part" "$UPSTREAM/$e/@v/$ver.$ext"; then mv "$f.part" "$f"; fetched=$((fetched+1))
    else rm -f "$f.part"; [ "$ext" = mod ] && { echo "could not fetch $mod $ver"; failed=$((failed+1)); }; fi
  done
  grep -qx "$ver" "$dir/list" 2>/dev/null || echo "$ver" >> "$dir/list"
done < <(sort -u "$ROOT/connector/go.sum")
echo "Fetched $fetched files into $DEST ($failed modules missing)."
echo "Now run: GOPROXY=file://$DEST GOSUMDB=off bash macos/scripts/update-helper.sh \"<identity>\""
