#!/bin/bash
set -euo pipefail
unset CDPATH GOROOT NODE_OPTIONS NODE_PATH GOFLAGS GOENV
unset DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH LD_PRELOAD LD_LIBRARY_PATH
export GOTOOLCHAIN=local
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"
[[ "$(uname -s)" == Darwin && "$(uname -m)" == arm64 ]] || {
  printf 'Native probe requires an Apple-silicon Mac.\n' >&2; exit 1;
}
. "$ROOT/scripts/toolchain-lib.sh"
GO="$(pager_select_go)" || {
  printf 'Stable Go 1.26+ for darwin/arm64 is required; no automatic download attempted.\n' >&2
  printf 'For guided setup run: bash scripts/setup-lan-macos.sh --donor\n' >&2
  exit 1
}
printf 'Building isolated research tools. No system settings or Nexal configuration will change.\n'
"$GO" version
umask 077
mkdir -p build
"$GO" test -race ./...
"$GO" vet ./...
"$GO" build -trimpath -o build/nexal-pager ./cmd/nexal-pager
"$GO" build -trimpath -o build/nexal-pager-lab ./cmd/nexal-pager-lab
xcrun clang -std=c11 -O2 -Wall -Wextra -Werror -arch arm64 \
  -mmacosx-version-min=13.0 -framework Hypervisor native/hvf_pager.c -o build/hvf-pager
codesign --force --sign - --entitlements native/entitlements.plist build/hvf-pager
codesign --verify --strict --verbose=2 build/hvf-pager
printf '\nBuild complete. Native execution has NOT yet been tested.\n'
printf 'Next: ./build/nexal-pager selftest --native-helper ./build/hvf-pager\n'
