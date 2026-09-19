#!/bin/bash
set -euo pipefail
unset CDPATH GOROOT NODE_OPTIONS NODE_PATH
unset DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH LD_PRELOAD LD_LIBRARY_PATH
export GOTOOLCHAIN=local
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
. "$ROOT/scripts/debug-lib.sh"
pager_debug_parse "$@"
if [[ "$PAGER_DEBUG" == true ]]; then
  export NEXAL_PAGER_DEBUG_SNAPSHOT=1
  pager_debug_run receiver "${BASH_SOURCE[0]}" ${PAGER_ARGS[@]+"${PAGER_ARGS[@]}"}
  exit
fi
if [[ $# -lt 1 ]]; then
  printf 'Usage: bash scripts/lan-receiver-macos.sh "/path/to/transferred/client" [--debug] [options]\n' >&2
  exit 1
fi
# Resolve the owner's path before changing to the module directory.
BUNDLE="$(cd -- "$1" && pwd -P)"
shift
cd "$ROOT"
bash scripts/build-macos.sh
if [[ "${NEXAL_PAGER_DEBUG_SNAPSHOT:-}" == 1 ]]; then
  PAGER_DEBUG=true
  pager_debug_snapshot
fi
printf '\nRunning one portable and three native CPU-paging cases.\n'
printf 'This is not a full guest OS and cannot add macOS host RAM or GPU memory.\n'
exec ./build/nexal-pager-lab receive --bundle "$BUNDLE" \
  --native-helper "$ROOT/build/hvf-pager" "$@"
