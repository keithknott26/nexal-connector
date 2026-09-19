#!/bin/bash
# Sourced helpers; no installation or shell-profile changes.
pager_go_valid() {
  local output major minor
  [[ -x "$1" ]] || return 1
  output="$(GOTOOLCHAIN=local "$1" version)" || return 1
  [[ "$output" =~ ^go[[:space:]]version[[:space:]]go([0-9]+)\.([0-9]+)(\.[0-9]+)?[[:space:]]darwin/arm64$ ]] || return 1
  major="${BASH_REMATCH[1]}"
  minor="${BASH_REMATCH[2]}"
  (( 10#$major > 1 || (10#$major == 1 && 10#$minor >= 26) ))
}

pager_select_go() {
  local candidate
  if [[ -n "${NEXAL_PAGER_GO:-}" ]]; then
    [[ "$NEXAL_PAGER_GO" == /* ]] && pager_go_valid "$NEXAL_PAGER_GO" || {
      printf 'NEXAL_PAGER_GO must name an absolute executable path to stable Go 1.26+ for darwin/arm64.\n' >&2
      return 1
    }
    printf '%s\n' "$NEXAL_PAGER_GO"
    return
  fi
  for candidate in /opt/homebrew/opt/go@1.26/bin/go "$(command -v go || true)"; do
    [[ "$candidate" == /* ]] || continue
    if pager_go_valid "$candidate"; then printf '%s\n' "$candidate"; return; fi
  done
  return 1
}
