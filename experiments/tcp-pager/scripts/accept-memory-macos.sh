#!/bin/bash
# Single-machine acceptance entry point. A separate LAN suite follows later.
set -euo pipefail
unset CDPATH GOROOT NODE_OPTIONS NODE_PATH
unset DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH LD_PRELOAD LD_LIBRARY_PATH
export GOTOOLCHAIN=local
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"
bash scripts/build-macos.sh
printf '\n=== Portable paging baseline ===\n'
./build/nexal-pager selftest | tee build/portable-acceptance.json
printf '\n=== Native CPU paging baseline ===\n'
./build/nexal-pager selftest --native-helper ./build/hvf-pager | tee build/native-acceptance.json
printf '\n=== CFAllocatorCreate callback and scope diagnostic ===\n'
bash scripts/test-cfallocator-macos.sh
printf '\n=== Requested OS-visible RAM requirement ===\n'
printf 'Collecting actual host RAM counters before, during and after paging.\n'
printf 'Exit 3 means paging passed but the requested OS integration is absent.\n'
exec ./build/nexal-pager-lab local-ram-test --native-helper "$ROOT/build/hvf-pager"
