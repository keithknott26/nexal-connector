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
NAME_EXPLICIT=false
PREPARE=false
PROFILE=""
CONFIG_EXPLICIT=false
usage() {
  printf 'Usage: bash setup-lan-macos.sh --enroll-platform [--name NAME] [--profile NAME | --config ABSOLUTE_PATH] [--coordinator HTTPS_ORIGIN] [--prepare-only]\n'
  printf 'A named profile preserves the default configuration and uses separate Keychain credentials.\n'
}
while [[ $# -gt 0 ]]; do
  case "$1" in
    --name|--coordinator|--config|--profile)
      [[ $# -ge 2 && -n "$2" && "$2" != --* ]] || { usage >&2; exit 2; }
      case "$1" in
        --name) NAME="$2"; NAME_EXPLICIT=true;;
        --coordinator) COORDINATOR="$2";;
        --config) CONFIG="$2"; CONFIG_EXPLICIT=true;;
        --profile) PROFILE="$2";;
      esac
      shift 2;;
    --prepare-only) PREPARE=true; shift;;
    --help|-h) usage; exit 0;;
    *) usage >&2; exit 2;;
  esac
done
if [[ -n "$PROFILE" ]]; then
  [[ "$CONFIG_EXPLICIT" == false && "$PROFILE" =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$ ]] || {
    printf 'Use a profile name of 1-48 letters, digits, underscores or hyphens, starting with a letter or digit. Do not combine --profile and --config.\n' >&2
    exit 2
  }
  CONFIG="$HOME/Library/Application Support/Nexal-Profiles/$PROFILE/config.json"
  printf 'Selected separate enrollment profile: %s\nOriginal profiles and identities will not be replaced or revoked.\n' "$PROFILE"
fi
[[ "$CONFIG" == /* && "$COORDINATOR" == https://* ]] || {
  printf 'An absolute config path and HTTPS coordinator are required.\n' >&2; exit 2;
}
COORDINATOR="${COORDINATOR%/}"
[[ ! -L "$CONFIG" ]] || { printf 'Refusing a symlink configuration.\n' >&2; exit 1; }
# Reject symlink parents too: profiles must not alias another profile/Keychain path.
PARENT="$(dirname -- "$CONFIG")"
while [[ "$PARENT" != / ]]; do
  [[ ! -L "$PARENT" ]] || { printf 'Refusing a symlink configuration parent.\n' >&2; exit 1; }
  PARENT="$(dirname -- "$PARENT")"
done
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
  SAVED_NAME="$(/usr/bin/plutil -extract name raw -o - "$CONFIG" 2>/dev/null || true)"
  printf '\nSaved host ID: %s\nSaved name: %s\nCoordinator: %s\nConfiguration: %s\n' "$HOST_ID" "$SAVED_NAME" "$COORDINATOR" "$CONFIG"
  printf 'An enrollment is already recorded. No invitation consumed or identity replaced.\n'
  printf 'This does not verify current coordinator access or credential validity.\n'
  printf 'Match this exact host ID in the shared dashboard. --name does not rename or re-enroll a saved identity.\n'
  printf 'For a new identity, choose an unused --profile NAME; keep that same profile for subsequent commands.\n'
  if [[ "$NAME_EXPLICIT" == true && -n "$SAVED_NAME" && "$SAVED_NAME" != "$NAME" ]]; then
    printf 'Requested name differs from the saved identity; stopping without changes.\n' >&2
    exit 1
  fi
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
printf 'Use this configuration for bundle transfer: %s\n' "$CONFIG"
printf 'Refresh Hosts in the shared dashboard and match the hostId printed above before enrolling the next Mac.\n'
