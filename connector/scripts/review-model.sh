#!/bin/sh
# Review a model snapshot for the next release: pin one exact Hugging Face commit, hash every file, and
# merge the result into internal/inference/reviewed-pins.json (which ships inside the app).
#
#   connector/scripts/review-model.sh <model-id> <40-hex-commit>
#
# Run it on a Mac with network access and a built `nexal` on PATH (or NEXAL=/path/to/nexal). Read the
# commit page on Hugging Face and the license first: the hashes are only as trustworthy as that review.
set -eu
[ $# -eq 2 ] || { echo "usage: $0 <model-id> <40-hex-commit>" >&2; exit 2; }
id=$1; rev=$2
here=$(cd "$(dirname "$0")" && pwd)
out="$here/../internal/inference/reviewed-pins.json"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
"${NEXAL:-nexal}" inference pin "$id" --revision "$rev" --json --config "$tmp/config.json" >/dev/null
python3 - "$tmp/inference/pins.json" "$out" "$id" <<'PY'
import json, sys
src, dst, mid = sys.argv[1:]
pin = json.load(open(src))["pins"][mid]
pin.pop("pinnedAt", None)  # keep the shipped file stable between reviews
cur = json.load(open(dst))
cur["pins"][mid] = pin
cur["pins"] = dict(sorted(cur["pins"].items()))
json.dump(cur, open(dst, "w"), indent=2); open(dst, "a").write("\n")
print("updated", dst, "->", mid, pin["revision"])
PY
echo "Review the diff of reviewed-pins.json against the Hugging Face commit page before committing."
