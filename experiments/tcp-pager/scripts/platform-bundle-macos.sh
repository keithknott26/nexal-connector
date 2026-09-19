#!/bin/bash
# Opt-in relay using existing enrolled host credentials. No automatic enrollment.
set -euo pipefail
unset CDPATH GOROOT NODE_OPTIONS NODE_PATH GOFLAGS GOENV
unset DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH LD_PRELOAD LD_LIBRARY_PATH
export GOTOOLCHAIN=local
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ $# -lt 1 || ( "$1" != receive && "$1" != send ) ]]; then
  printf 'Usage: bash platform-bundle-macos.sh receive --config PATH --from-host HOST_ID\n'
  printf '   or: bash platform-bundle-macos.sh send --config PATH --transfer ID --receiver-key-sha256 HASH --bundle PATH\n'
  exit 2
fi
MODE="$1"
shift
# Reuse consent-based prerequisites. --no-start prevents a second donor listener.
bash "$ROOT/scripts/setup-lan-macos.sh" --donor --no-start
. "$ROOT/scripts/toolchain-lib.sh"
GO="$(pager_select_go)"
umask 077
mkdir -p "$ROOT/build"
(cd "$ROOT/../../connector" && "$GO" build -trimpath -o "$ROOT/build/nexal-transfer" ./cmd/nexal)
if [[ "$MODE" == send ]]; then
  exec "$ROOT/build/nexal-transfer" bundle-send "$@"
fi
# stderr carries PUBLIC pairing metadata. stdout carries only the private folder
# path; no private keys are printed. The pager still verifies the donor CA.
BUNDLE="$("$ROOT/build/nexal-transfer" bundle-receive "$@" --path-only)"
[[ -n "$BUNDLE" && "$BUNDLE" == /* && -d "$BUNDLE" ]] || {
  printf 'No private receiver folder returned; stopping.\n' >&2; exit 1;
}
exec bash "$ROOT/scripts/setup-lan-macos.sh" --receiver "$BUNDLE"
