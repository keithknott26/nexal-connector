#!/bin/bash
# Source this file from package-app.sh. Resolve only the policy-pinned release.
if [ -z "${NEXAL_CODE_SIGN_IDENTITY:-}" ]; then
  NEXAL_CODE_SIGN_IDENTITY="$(/usr/bin/security find-identity -v -p codesigning | /usr/bin/sed -n 's/.*"\(Developer ID Application:.*(GHN7W3WALP)\)".*/\1/p' | head -n 1)"
fi
if [ -z "$NEXAL_CODE_SIGN_IDENTITY" ]; then
  echo 'Missing neXal Developer ID signing identity (team GHN7W3WALP).' >&2
  echo 'Use a signed DMG from the release Mac, or install your authorized signing certificate and private key in Keychain before building.' >&2
  exit 1
fi
if [ -z "${NEXAL_MESH_RUNTIME_ARTIFACT:-}" ]; then
  command -v gh >/dev/null || { echo 'Install GitHub CLI and run gh auth login to download the approved runtime from the private repository.' >&2; exit 1; }
  RUNTIME_CACHE="$ROOT/build/runtime-mlkem1024-recovery7"
  mkdir -p "$RUNTIME_CACHE"
  NEXAL_MESH_RUNTIME_ARTIFACT="$RUNTIME_CACHE/nexal-network"
  if ! python3 "$ROOT/scripts/verify-runtime.py" source "$NEXAL_MESH_RUNTIME_ARTIFACT" >/dev/null 2>&1; then
    RUNTIME_DOWNLOAD="$(mktemp -d "$RUNTIME_CACHE/download.XXXXXX")"
    if ! gh release download runtime-mlkem1024-recovery7 --repo keithknott26/nexal-connector --pattern nexal-network --dir "$RUNTIME_DOWNLOAD"; then
      rm -rf "$RUNTIME_DOWNLOAD"
      echo 'Runtime download failed. Run gh auth login with access to nexal-connector, then retry.' >&2
      exit 1
    fi
    if ! python3 "$ROOT/scripts/verify-runtime.py" source "$RUNTIME_DOWNLOAD/nexal-network"; then
      rm -rf "$RUNTIME_DOWNLOAD"
      exit 1
    fi
    mv "$RUNTIME_DOWNLOAD/nexal-network" "$NEXAL_MESH_RUNTIME_ARTIFACT"
    rmdir "$RUNTIME_DOWNLOAD"
  fi
fi
export NEXAL_MESH_RUNTIME_ARTIFACT NEXAL_CODE_SIGN_IDENTITY
