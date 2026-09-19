#!/bin/bash
# Explicit allowlisted diagnostics, never shell tracing or environment dumps.
pager_debug_parse() {
  PAGER_DEBUG=false
  PAGER_ARGS=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --debug) PAGER_DEBUG=true ;;
      *) PAGER_ARGS+=("$1") ;;
    esac
    shift
  done
}

pager_debug_snapshot() {
  [[ "$PAGER_DEBUG" == true ]] || return 0
  printf '\n[debug] UTC time: '
  date -u '+%Y-%m-%dT%H:%M:%SZ'
  printf '[debug] OS/architecture: '
  uname -sm
  printf '[debug] Checkout revision: '
  git -C "$ROOT" rev-parse --short HEAD || true
  printf '[debug] Active private interfaces (not proof of peer reachability):\n'
  "$ROOT/build/nexal-pager-lab" networks || true
  if [[ -x /usr/sbin/lsof ]]; then
    printf '[debug] Existing TCP 9443 listeners (empty means none visible):\n'
    /usr/sbin/lsof -nP -iTCP:9443 -sTCP:LISTEN || true
  fi
  printf '[debug] Starting requested operation; watch for connection phase/result events.\n'
}

pager_debug_run() {
  local role="$1"
  shift
  if [[ "$PAGER_DEBUG" != true ]]; then
    bash "$@"
    return
  fi
  local directory status
  umask 077
  directory="$(mktemp -d "${TMPDIR:-/tmp}/nexal-pager-${role}.XXXXXX")"
  printf '\n[debug] Live output is also saved to:\n%s/debug.log\n' "$directory"
  printf '[debug] Logs contain local paths/IPs. Review before sharing; never share key files.\n'
  # Preserve stdin for interactive prompts; only stdout/stderr go through tee.
  # Execute in an independent shell so errexit remains effective inside scripts.
  (
    printf '[debug] Role: %s\n' "$role"
    if bash "$@"; then status=0; else status=$?; fi
    printf '\n[debug] Script exit status: %s\n' "$status"
    exit "$status"
  ) 2>&1 | /usr/bin/tee "$directory/debug.log"
}
