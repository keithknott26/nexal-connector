#!/bin/bash
# Pinned official BSD-3-Clause runtime. Called by package-app.sh with private
# staging and candidate directories; never installs software on the host.
set -euo pipefail
STAGE="$1"
CANDIDATE="$2"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION=1.20.0
for ARCH in aarch64 x86_64; do
  case "$ARCH" in
    aarch64) EXPECTED=33685a589133c5112611c06b66a14f759c8788347dc438795ec895b214e2897a ;;
    x86_64) EXPECTED=4d06b46eea0231897a4e3561148a95d5895f74286f6dfe5f9da90e0e4fd36007 ;;
  esac
  ARCHIVE="$STAGE/yara-x-$ARCH.tar.gz"
  /usr/bin/curl --fail --location --proto '=https' --proto-redir '=https' \
    --connect-timeout 15 --max-time 180 \
    -o "$ARCHIVE" "https://github.com/VirusTotal/yara-x/releases/download/v$VERSION/yara-x-v$VERSION-$ARCH-apple-darwin.tar.gz"
  printf '%s  %s\n' "$EXPECTED" "$ARCHIVE" | /usr/bin/shasum -a 256 -c -
  mkdir "$STAGE/yara-x-$ARCH"
  /usr/bin/tar -xzf "$ARCHIVE" -C "$STAGE/yara-x-$ARCH" yr
  test -f "$STAGE/yara-x-$ARCH/yr"
done
/usr/bin/lipo -create "$STAGE/yara-x-aarch64/yr" "$STAGE/yara-x-x86_64/yr" -output "$CANDIDATE/Contents/Helpers/yr"
chmod 755 "$CANDIDATE/Contents/Helpers/yr"
# Hardened-runtime YARA-X scanning needs the dedicated executable-memory
# entitlements in YARA-X.entitlements; they apply only to this helper.
install -m 644 "$ROOT/../connector/internal/cybersecurity/rules/LICENSE" "$CANDIDATE/Contents/Resources/NEXAL-RULES-LICENSE.txt"
install -m 644 "$ROOT/Resources/YARA-X-LICENSE.txt" "$CANDIDATE/Contents/Resources/YARA-X-LICENSE.txt"
test "$("$CANDIDATE/Contents/Helpers/yr" --version)" = "yara-x-cli $VERSION"
