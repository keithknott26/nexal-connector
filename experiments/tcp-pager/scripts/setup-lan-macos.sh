#!/bin/bash
# Restart-safe owner-run setup. No persistent credentials or automatic login tasks.
set -euo pipefail
unset CDPATH GOROOT NODE_OPTIONS NODE_PATH GOFLAGS GOENV
unset DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH LD_PRELOAD LD_LIBRARY_PATH
export GOTOOLCHAIN=local
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ "${1:-}" == --enroll-platform ]]; then
  shift
  exec bash "$ROOT/scripts/enroll-platform-macos.sh" "$@"
fi
. "$ROOT/scripts/toolchain-lib.sh"
MODE=""
BUNDLE=""
CHECK=false
START=true
usage() {
  printf 'Usage: bash setup-lan-macos.sh --donor|--receiver "/path/to/client" [--check] [--no-start]\n'
  printf '   or: bash setup-lan-macos.sh --network-info [--check]\n'
  printf '   or: bash setup-lan-macos.sh --enroll-platform [--name NAME] [--coordinator HTTPS_ORIGIN] [--config ABSOLUTE_PATH] [--prepare-only]\n'
}
while [[ $# -gt 0 ]]; do
  case "$1" in
    --donor)
      [[ -z "$MODE" ]] || { usage >&2; exit 2; }
      MODE=donor; shift ;;
    --network-info)
      [[ -z "$MODE" ]] || { usage >&2; exit 2; }
      MODE=network-info; shift ;;
    --receiver)
      [[ -z "$MODE" && $# -ge 2 && "$2" != --* ]] || { usage >&2; exit 2; }
      MODE=receiver
      BUNDLE="$(cd -- "$2" && pwd -P)"
      shift 2 ;;
    --check) CHECK=true; shift ;;
    --no-start) START=false; shift ;;
    --help|-h) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done
[[ -n "$MODE" ]] || { usage >&2; exit 2; }
[[ "$(uname -s)" == Darwin && "$(uname -m)" == arm64 ]] || {
  printf 'Use a native Terminal on an Apple-silicon Mac.\n' >&2; exit 1;
}
printf '\nNexal private LAN research setup (%s)\n' "$MODE"
printf 'Safe command to rerun after interruption or reboot:\n  bash %q --%s' "$ROOT/scripts/setup-lan-macos.sh" "$MODE"
[[ "$MODE" != receiver ]] || printf ' %q' "$BUNDLE"
[[ "$START" == true ]] || printf ' --no-start'
[[ "$CHECK" == false ]] || printf ' --check'
printf '\nNo reboot is requested by this script. If macOS or Apple tooling requests one,\ncomplete it and rerun the command above. No donor is started automatically at login.\n'

# Validate Apple build tooling before offering package changes.
APPLE_READY=true
if ! xcrun --find clang || ! xcrun --sdk macosx --show-sdk-path || ! command -v codesign; then
  APPLE_READY=false
  printf 'Apple build tools/SDK are not ready. Complete their installation and license prompts, then rerun.\n' >&2
fi
GO=""
if GO="$(pager_select_go)"; then
  printf 'Selected Go: %s\n' "$GO"
else
  printf 'Stable Go 1.26+ for darwin/arm64 is required.\n'
fi
if [[ "$CHECK" == true ]]; then
  printf 'Read-only check finished. No packages installed, builds run, or donor started.\n'
  [[ "$APPLE_READY" == true && -n "$GO" ]]
  exit
fi
[[ "$APPLE_READY" == true ]] || exit 1
if [[ -z "$GO" ]]; then
  [[ -z "${NEXAL_PAGER_GO:-}" ]] || {
    printf 'Correct or unset NEXAL_PAGER_GO before retrying; no package changes made.\n' >&2; exit 1;
  }
  BREW="$(command -v brew || true)"
  [[ -x /opt/homebrew/bin/brew ]] && BREW=/opt/homebrew/bin/brew
  [[ -n "$BREW" ]] || {
    printf 'Homebrew is missing. Review/install it from https://brew.sh, then rerun this script.\n' >&2; exit 1;
  }
  printf '\nInstall go@1.26 through Homebrew? Dependencies may also be installed or upgraded.\n'
  printf 'Nexal will not run brew link, edit shell profiles, accept Apple licenses, or reboot.\n'
  printf 'Install now? [y/N] '
  answer=""
  IFS= read -r answer || true
  case "$answer" in
    y|Y|yes|YES) ;;
    *) printf 'No packages installed. Rerun whenever ready.\n'; exit 1 ;;
  esac
  "$BREW" install go@1.26 || {
    printf 'Homebrew did not complete. Resolve the reported error, then rerun; do not bypass checks.\n' >&2; exit 1;
  }
  PREFIX="$("$BREW" --prefix go@1.26)"
  [[ "$PREFIX" == /* ]] || { printf 'Homebrew returned an invalid prefix.\n' >&2; exit 1; }
  GO="$PREFIX/bin/go"
  pager_go_valid "$GO" || {
    printf 'Installed Go did not pass version/architecture validation. No donor started.\n' >&2; exit 1;
  }
fi
export NEXAL_PAGER_GO="$GO"
export PATH="$(dirname -- "$GO"):$PATH"
printf '\nUsing Go only for this setup and its child processes:\n'
"$GO" version
if [[ "$START" == false ]]; then
  printf 'Prerequisites ready. No build or network listener started. Rerun without --no-start when ready.\n'
  exit 0
fi
if [[ "$MODE" == network-info ]]; then
  printf '\nListing active private IP addresses and connection types. No listener will start.\n'
  umask 077
  mkdir -p "$ROOT/build"
  (cd "$ROOT" && "$GO" build -trimpath -o build/nexal-pager-network-info ./cmd/nexal-pager-lab)
  exec "$ROOT/build/nexal-pager-network-info" networks
fi
printf '\nStarting the bounded private test. After any donor restart, transfer its NEW client folder.\n'
if [[ "$MODE" == donor ]]; then
  exec bash "$ROOT/scripts/lan-donor-macos.sh"
else
  exec bash "$ROOT/scripts/lan-receiver-macos.sh" "$BUNDLE"
fi
