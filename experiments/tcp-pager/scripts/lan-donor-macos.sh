#!/bin/bash
set -euo pipefail
unset CDPATH GOROOT NODE_OPTIONS NODE_PATH
unset DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH LD_PRELOAD LD_LIBRARY_PATH
export GOTOOLCHAIN=local
ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$ROOT"
bash scripts/build-macos.sh
printf '\nPreparing a PRIVATE, disposable 1 MiB-per-session donor.\n'
printf 'No OS memory settings, firewall rules, enrollment or public sharing will change.\n'
exec ./build/nexal-pager-lab donor --show-folder "$@"
