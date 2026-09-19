#!/bin/bash
# Explicit, bounded diagnostic restart with existing credentials. No re-enrollment.
set -euo pipefail
unset CDPATH GOROOT NODE_OPTIONS NODE_PATH GOFLAGS GOENV
unset DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH LD_PRELOAD LD_LIBRARY_PATH
export GOTOOLCHAIN=local
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
. "$ROOT/scripts/debug-lib.sh"
pager_debug_parse "$@"
if [[ "$PAGER_DEBUG" == true ]]; then
  export NEXAL_PAGER_DEBUG_SNAPSHOT=1
  pager_debug_run donor-resume "${BASH_SOURCE[0]}" ${PAGER_ARGS[@]+"${PAGER_ARGS[@]}"}
  exit
fi
if [[ $# -ne 1 || "$1" != /* ]]; then
  printf 'Usage: bash resume-donor-macos.sh "/absolute/existing/donor-state/run" [--debug]\n' >&2
  exit 2
fi
STATE="$1"
[[ -f "$STATE/donor/connection.json" && -f "$STATE/client/connection.json" ]] || {
  printf 'Existing donor state is missing; no keys generated or overwritten.\n' >&2
  exit 1
}
printf 'Stop only the previous pager donor with Ctrl+C before continuing.\n'
printf 'This resumes its existing certificates for at most 30 minutes, with TLS diagnostics.\n'
printf 'No processes will be killed. No system settings or enrollment will change.\n'
printf 'The endpoint must still belong to this Mac and certificates must remain valid.\n'
bash "$ROOT/scripts/build-macos.sh"
if [[ "${NEXAL_PAGER_DEBUG_SNAPSHOT:-}" == 1 ]]; then
  PAGER_DEBUG=true
  pager_debug_snapshot
fi
umask 077
LOG="$(mktemp "$STATE/donor-diagnostics.XXXXXX")"
printf '\nDonor diagnostics will also be saved here:\n%s\n' "$LOG"
printf 'After one receiver attempt, read this log from another Terminal window.\n'
# Fixed-label TLS events and startup information only; never copy key files.
# pipefail preserves donor failure rather than reporting tee's success.
"$ROOT/build/nexal-pager-lab" serve --state "$STATE" --lifetime 30m --sessions 16 2>&1 |
  /usr/bin/tee "$LOG"
