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
NO_BROWSER=false
PROFILE=""
CONFIG_EXPLICIT=false
usage() {
  printf 'Usage: bash setup-lan-macos.sh --enroll-platform [--name NAME] [--profile NAME | --config ABSOLUTE_PATH] [--coordinator HTTPS_ORIGIN] [--prepare-only] [--no-browser]\n'
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
    --no-browser) NO_BROWSER=true; shift;;
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
# Only an HTTPS origin may be opened. Never forward credentials, query strings,
# fragments or paths from arguments/configuration to the default browser.
[[ "$COORDINATOR" =~ ^https://([A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?$ ]] || {
  printf 'Coordinator must be an HTTPS origin without credentials, paths, query strings or fragments.\n' >&2
  exit 2
}
DASHBOARD="$COORDINATOR/#/hosts"
open_dashboard() {
  printf '\nDashboard: %s\n' "$DASHBOARD"
  printf 'Enter your Nexal OWNER credential in the browser sign-in form, not in Terminal.\n'
  if [[ "$NO_BROWSER" == true ]]; then
    printf 'Automatic browser opening disabled; open the dashboard address manually.\n'
  elif ! /usr/bin/open "$DASHBOARD"; then
    printf 'Could not open the default browser. Open the dashboard address above manually; setup can continue.\n' >&2
  fi
}
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
  if [[ "$PREPARE" == false && -t 0 ]]; then
    open_dashboard
    printf 'An identity is already saved; compare its host ID in Hosts. No new invitation is needed for this rerun.\n'
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
open_dashboard
printf 'After signing in, use Hosts > Enroll host > Generate invitation, then return to this Terminal.\n'
printf 'Use that shared dashboard, not localhost. Do not paste the invitation into chat.\n'
printf 'The Terminal prompt accepts the ONE-USE INVITATION, not the owner credential.\n'
printf 'Paste the generated enr_ invitation, NOT the owner token (input hidden): '
CODE=""
if ! IFS= read -r -s CODE; then
  printf '\nNo complete code received; configuration preserved.\n' >&2
  exit 1
fi
printf '\n'
[[ -n "$CODE" && ${#CODE} -le 256 ]] || {
  printf 'Empty or oversized invitation; no enrollment attempted.\n' >&2; exit 1;
}
# Current coordinator invitations are enr_ followed by 32 bytes in hex.
# This catches accidental owner-token pastes without transmitting or echoing them.
[[ "$CODE" =~ ^enr_[0-9a-f]{64}$ ]] || {
  unset CODE
  printf 'Not a Nexal enrollment invitation. Nothing was submitted.\n' >&2
  printf 'Use the owner token only in the browser. In Hosts > Enroll host > Generate invitation, copy the generated enr_ code, then rerun this same command.\n' >&2
  exit 1
}
if printf '%s\n' "$CODE" | "$BIN" enroll --code-stdin --config "$CONFIG"; then
  unset CODE
else
  STATUS=$?
  unset CODE
  printf 'Enrollment did not complete. HTTP 409 means the invitation is invalid, expired, or already used; generate a fresh invitation in the same coordinator dashboard and rerun this command.\n' >&2
  printf 'For other errors, resolve the reported cause first. Do not delete the profile or paste the owner token here.\n' >&2
  exit "$STATUS"
fi
printf 'Enrollment complete. No agent or job started; existing donor was left alone.\n'
printf 'Use this configuration for bundle transfer: %s\n' "$CONFIG"
printf 'Refresh Hosts in the shared dashboard and match the hostId printed above before enrolling the next Mac.\n'
