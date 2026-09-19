#!/bin/bash
set -euo pipefail
unset CDPATH
unset DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH LD_PRELOAD LD_LIBRARY_PATH
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"
[[ "$(uname -s)" == Darwin && "$(uname -m)" == arm64 ]] || {
  printf 'This diagnostic requires an Apple-silicon Mac.\n' >&2; exit 1;
}
umask 077
mkdir -p build
xcrun clang -std=c11 -O2 -Wall -Wextra -Werror -arch arm64 \
  tests/test_bounded_alloc.c -o build/test-bounded-alloc
./build/test-bounded-alloc
xcrun clang -std=c11 -O2 -Wall -Wextra -Werror -arch arm64 \
  -framework CoreFoundation native/cfallocator_probe.c -o build/cfallocator-probe
printf 'Testing real Core Foundation callbacks with bounded LOCAL allocations.\n'
printf 'This is not a remote pager or a system RAM expansion implementation.\n'
./build/cfallocator-probe | tee build/cfallocator-acceptance.json
