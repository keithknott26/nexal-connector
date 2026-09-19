#!/bin/bash
set -euo pipefail
unset CDPATH
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"
bash scripts/build-macos.sh
printf '\n=== Portable transport/cache acceptance (loopback) ===\n'
./build/nexal-pager selftest | tee build/portable-acceptance.json
printf '\n=== Native CPU-fault acceptance (loopback donor, not another Mac yet) ===\n'
./build/nexal-pager selftest --native-helper ./build/hvf-pager | tee build/native-acceptance.json
printf '\nBoth commands passed. These results do not demonstrate macOS guest boot or added host/GPU RAM.\n'
