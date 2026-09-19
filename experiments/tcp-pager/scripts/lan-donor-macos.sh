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
  pager_debug_run donor "${BASH_SOURCE[0]}" ${PAGER_ARGS[@]+"${PAGER_ARGS[@]}"}
  exit
fi
cd "$ROOT"
bash scripts/build-macos.sh
if [[ "${NEXAL_PAGER_DEBUG_SNAPSHOT:-}" == 1 ]]; then
  PAGER_DEBUG=true
  pager_debug_snapshot
fi
printf '\nPreparing a PRIVATE, disposable 1 MiB-per-session donor.\n'
printf 'No OS memory settings, firewall rules, enrollment or public sharing will change.\n'
exec ./build/nexal-pager-lab donor --show-folder "$@"
