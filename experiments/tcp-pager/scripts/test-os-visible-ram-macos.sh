#!/bin/bash
set -euo pipefail
unset CDPATH
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
printf 'Requested requirement: additional usable OS-visible RAM.\n'
printf 'This implementation does NOT satisfy that requirement.\n'
printf 'The test records real hw.memsize before/during/after paging and exits 3 if paging\n'
printf 'passes but OS-visible RAM integration is absent. It never changes counters.\n'
if [[ $# -eq 0 ]]; then
  cd "$ROOT"
  bash scripts/build-macos.sh
  exec ./build/nexal-pager-lab local-ram-test --native-helper "$ROOT/build/hvf-pager"
fi
exec bash "$ROOT/scripts/lan-receiver-macos.sh" "$@" --require-os-ram
