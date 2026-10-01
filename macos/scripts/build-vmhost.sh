#!/usr/bin/env bash
# Build nexal-vmhost and ad-hoc sign it with the virtualization entitlement.
# Works without a paid developer account.
set -euo pipefail
cd "$(dirname "$0")/.."

swift build -c release --product nexal-vmhost
BIN="$(swift build -c release --product nexal-vmhost --show-bin-path)/nexal-vmhost"

codesign --force --sign - --entitlements NexalVMHost.entitlements "$BIN"
if ! codesign -d --entitlements :- "$BIN" 2>/dev/null | grep -q "com.apple.security.virtualization"; then
  echo "error: virtualization entitlement missing from $BIN" >&2
  exit 1
fi
echo "$BIN"
