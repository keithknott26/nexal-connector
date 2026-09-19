#!/bin/bash
# Explicit owner enrollment, separate from the memory donor and receiver.
set +x
set -euo pipefail
unset CDPATH GOROOT GOFLAGS GOENV BASH_ENV ENV
unset NODE_OPTIONS NODE_PATH DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH
unset LD_PRELOAD LD_LIBRARY_PATH
export GOTOOLCHAIN=local
umask 077
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
COORDINATOR="https://nexal-coordinator-dev.nexal.systems"
CONFIG="$HOME/Library/Application Support/Nexal/config.json"
NAME="Nexal Mac"
PREPARE=false
usage() {
  printf 'Usage: bash setup-lan-macos.sh --enroll-platform [--name NAME] [--coordinator HTTPS_ORIGIN] [--config ABSOLUTE_PATH] [--prepare-only]\n'
}
while [[ $# -gt 0 ]]; do
  case "$1" in
    --name|--coordinator|--config)
      [[ $# -ge 2 && -n "$2" && "$2" != --* ]] || { usage >&2; exit 2; }
      case "$1" in
        --name) NAME="$2";;
        --coordinator) COORDINATOR="$2";;
        --config) CONFIG="$2";;
      esac
      shift 2;;
    --prepare-only) PREPARE=true; shift;;
    --help|-h) usage; exit 0;;
    *) usage >&2; exit 2;;
  esac
done
[[ "$CONFIG" == /* && "$COORDINATOR" == https://* ]] || {
  printf 'An absolute config path and HTTPS coordinator are required.\n' >&2; exit 2;
}
COORDINATOR="${COORDINATOR%/}"
[[ ! -L "$CONFIG" ]] || { printf 'Refusing a symlink configuration.\n' >&2; exit 1; }
# Refuse mismatched profiles before any package installation or build.
if [[ -e "$CONFIG" ]]; then
  [[ -f "$CONFIG" ]] || { printf 'Configuration is not a regular file.\n' >&2; exit 1; }
  EXISTING="$(/usr/bin/plutil -extract coordinator raw -o - "$CONFIG")" || {
    printf 'Cannot read saved coordinator; configuration preserved.\n' >&2; exit 1;
  }
  [[ "$EXISTING" == "$COORDINATOR" ]] || {
    printf 'Saved coordinator differs from the requested coordinator. Nothing changed.\n' >&2
    printf 'Choose the matching --coordinator or a separate --config; do not delete credentials.\n' >&2
    exit 1
  }
fi

# The reused prerequisite flow offers Homebrew changes only with consent.
# It does not start another donor, receiver, or agent.
bash "$ROOT/scripts/setup-lan-macos.sh" --donor --no-start
. "$ROOT/scripts/toolchain-lib.sh"
GO="$(pager_select_go)"
[[ ! -L "$ROOT/build" ]] || { printf 'Refusing symlink build directory.\n' >&2; exit 1; }
mkdir -p "$ROOT/build"
STAGE="$(mktemp -d "$ROOT/build/.enroll-build.XXXXXX")"
unset CODE
cleanup() {
  unset CODE
  rm -rf -- "$STAGE"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
(cd "$ROOT/../../connector" && "$GO" build -trimpath -o "$STAGE/nexal" ./cmd/nexal)
BIN="$ROOT/build/nexal-transfer"
[[ ! -L "$BIN" && ! -d "$BIN" ]] || { printf 'Unsafe build destination.\n' >&2; exit 1; }
mv -f "$STAGE/nexal" "$BIN"

if [[ ! -e "$CONFIG" ]]; then
  "$BIN" init --coordinator "$COORDINATOR" --name "$NAME" \
    --memory-limit-mib 256 --reserve-memory-mib 4096 --config "$CONFIG"
else
  printf 'Existing configuration and resource policy preserved.\n'
fi
# A saved ID is a local record, not a live credential/health verification.
# Missing hostId is normal before enrollment; malformed configs are rejected
# by the Go enrollment command, never repaired or overwritten here.
HOST_ID="$(/usr/bin/plutil -extract hostId raw -o - "$CONFIG" 2>/dev/null || true)"
if [[ -n "$HOST_ID" ]]; then
  printf 'An enrollment is already recorded. No invitation consumed or identity replaced.\n'
  printf 'This does not verify current coordinator access or credential validity.\n'
  exit 0
fi
printf '\nPrepared coordinator: %s\nConfiguration: %s\n' "$COORDINATOR" "$CONFIG"
printf 'No donor, receiver, agent, public sharing or jobs started.\n'
if [[ "$PREPARE" == true ]]; then
  printf 'Preparation complete. Rerun without --prepare-only when ready to enroll.\n'
  exit 0
fi
[[ -t 0 ]] || {
  printf 'Enrollment needs a terminal for hidden input. Rerun interactively or append </dev/tty.\n' >&2
  exit 1
}
printf '\nOpen %s in your browser and use Hosts > Enroll host > Generate invitation.\n' "$COORDINATOR"
printf 'Use that shared dashboard, not localhost. Do not paste the invitation into chat.\n'
printf 'Paste one-use code, then press Return (input hidden): '
CODE=""
if ! IFS= read -r -s CODE; then
  printf '\nNo complete code received; configuration preserved.\n' >&2
  exit 1
fi
printf '\n'
[[ -n "$CODE" && ${#CODE} -le 256 ]] || {
  printf 'Empty or oversized invitation; no enrollment attempted.\n' >&2; exit 1;
}
printf '%s\n' "$CODE" | "$BIN" enroll --code-stdin --config "$CONFIG"
unset CODE
printf 'Enrollment complete. No agent or job started; existing donor was left alone.\n'
