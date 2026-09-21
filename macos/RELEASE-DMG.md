# Producing the Nexal Connector .dmg

`.github/workflows/release-dmg.yml` builds a signed, notarized, stapled DMG.
It runs on `workflow_dispatch` or a `v*` tag only — never on an ordinary push,
because macOS runners bill at roughly 10x the Linux rate and notarization calls
Apple's service.

## What it does

1. Runs the Go and Swift test suites. A release is never the first place they run.
2. Calls `macos/scripts/package-app.sh`, the same recipe used locally, so local
   and release packaging cannot drift.
3. Signs inside-out: the embedded Go helper `Contents/Helpers/nexal` first, then
   the app bundle. Signing the bundle first would seal an unsigned executable and
   fail notarization.
4. Builds a compressed read-only UDZO DMG with an `/Applications` symlink.
5. Signs the DMG, submits it to Apple with `notarytool --wait`, staples the
   ticket so first launch works offline, then asserts the verdict with `spctl`.
6. Uploads the DMG and its SHA-256.

## Without signing secrets

The job still runs and produces an artifact named
`...-UNSIGNED-DO-NOT-DISTRIBUTE.dmg`. **Gatekeeper will refuse it on any other
Mac.** This exists so the pipeline is verifiable before certificates are
available. Publishing requires both a `v*` tag and a signed build, so an
unsigned DMG can never be attached to a release.

## Required repository secrets

| Secret | What it is |
|---|---|
| `APPLE_DEVELOPER_ID_APPLICATION_P12` | Base64 of the exported **Developer ID Application** `.p12`. `base64 -i cert.p12 \| pbcopy` |
| `APPLE_DEVELOPER_ID_APPLICATION_PASSWORD` | Password set when exporting that `.p12` |
| `APPLE_SIGNING_IDENTITY_NAME` | Name inside the identity, e.g. `KWK, LLC` — the part between `Developer ID Application: ` and ` (TEAMID)` |
| `APPLE_TEAM_ID` | 10-character Team ID from the Apple Developer account |
| `APPLE_NOTARY_APPLE_ID` | Apple ID email for notarization |
| `APPLE_NOTARY_PASSWORD` | **App-specific password**, not the Apple ID password. Generate at appleid.apple.com |

Needs the Apple Developer Program (99 USD/year). A **Developer ID Application**
certificate is required; "Mac Development" and "Mac App Distribution" will not
notarize for direct distribution.

Use an app-specific password rather than the account password, so revoking CI
access never means changing the Apple ID credential itself.

## Certificate handling

The `.p12` is imported into an **ephemeral keychain** in `$RUNNER_TEMP` with a
random password, and deleted in a step that runs `if: always()`. The certificate
never persists on the runner image, and no secret is echoed in any step.

## Release

```bash
# bump CFBundleShortVersionString in macos/Resources/Info.plist first
git tag v0.1.0 && git push origin v0.1.0
```

The DMG filename takes its version from `Info.plist`, so the tag and the bundle
version should be kept in step.

## Public-repo notes

This repository is intended to become public. `.github/workflows/secrets.yml`
runs gitleaks across full history on every push. Verified clean at the time of
writing: no credentials, no Cloudflare account or zone identifiers, no database
IDs. The `/Users/test/...` paths in `macos/Tests` are fixtures, not real paths.

Keep it that way: coordinator hostnames are fine to publish, but account
identifiers, database IDs and owner tokens belong only in `nexal-platform`
deployment config and Wrangler secrets.

## Not verified here

Nothing in this pipeline has been executed. There is no macOS runner, Swift
toolchain, Apple SDK or signing certificate in the environment where it was
written, so it is reviewed, not proven. Expect the first run to need iteration —
most likely on the exact `APPLE_SIGNING_IDENTITY_NAME` string, which must match
`security find-identity -v -p codesigning` on a machine holding the certificate.
