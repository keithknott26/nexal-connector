#!/bin/bash
set -euo pipefail
unset CDPATH GOROOT NODE_OPTIONS NODE_PATH
unset DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH LD_PRELOAD LD_LIBRARY_PATH
export GOTOOLCHAIN=local
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"
[[ "$(uname -s)" == Darwin && "$(uname -m)" == arm64 ]] || {
  printf 'Native probe requires an Apple-silicon Mac.\n' >&2; exit 1;
}
GO="$(command -v go || true)"
if [[ -x /opt/homebrew/opt/go@1.26/bin/go ]]; then GO=/opt/homebrew/opt/go@1.26/bin/go; fi
[[ -n "$GO" ]] || { printf 'Go 1.26+ is required; nothing installed automatically.\n' >&2; exit 1; }
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
