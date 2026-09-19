# Nexal Connector

For a resumable engineering checkpoint and the full platform document archive,
read [CURRENT-HANDOFF.md](CURRENT-HANDOFF.md).

Native Mac connector for the Nexal Platform, intended for nexal.systems.
Prepared for KWK, LLC. Private proprietary engineering preview, not a
production-ready marketplace worker, certified sandbox or verified PQ system.

## Components

- `connector/`: Go 1.26 CLI, owner policies, credentials, heartbeat, fixed CPU
  prototype, private-pool primitives and strict tunnel configuration.
- `macos/`: SwiftUI menu-bar app and unsigned source-build packaging.
- `runtimes/`: gated MLX adapter, integrity manifests and Go command bridge.
- `CONTRACT.md`: API v1 snapshot shared with the platform. Change both contracts
  and the `COMPATIBILITY` revision together for breaking integration changes.

## Setup on your Mac

Clone [Nexal Platform](https://github.com/keithknott26/nexal-platform) beside this
repository, authenticate to your private GitHub account, then run:

```sh
cd ../nexal-platform
bash scripts/setup-macos.sh --container
```

The coordinator runs in Docker. This repository supplies the native Go/Swift
application; a Linux container is not a substitute for macOS Keychain or Metal.
The installer asks before package-manager changes and keeps global tool links
untouched. Use `--native` instead for a host-run local coordinator.

The resulting app is `macos/build/Nexal Connector.app`. It is not signed or
notarized for distribution. Leave Gatekeeper and SIP enabled.

## Local preview enrollment

After an owner-reviewed native build, open the app and expand setup:

1. Choose **Use bundled nexal**.
2. Enable **Use local development preview** explicitly.
3. Generate an invitation in the local platform's Hosts screen and paste it
   into the app's secure field.
4. Approve private-only enrollment and choose **Create and enroll**.
5. Choose **Start / Connect**. Leave contribution paused while checking telemetry.

Local preview uses numeric loopback only and separate
`~/Library/Application Support/Nexal-Local-Preview` configuration with restricted
file credentials, not Keychain. Production configuration uses
`~/Library/Application Support/Nexal` and requires HTTPS. Switching profiles is
blocked while attached/running. No public sharing, synthetic telemetry, paid
cloud fallback or job dispatch is enabled by this UI.

The local-preview UI and its XCTest cases are written but have not been compiled
or run on a Mac in this environment. Go policies and Python integrity tests were
tested separately. Report actual Mac build failures; do not bypass safeguards.

## Verification

```sh
(cd connector && go vet ./... && go test -race ./...)
(cd runtimes/bridge && go vet ./... && go test -race ./...)
PYTHONPATH=runtimes python -m unittest discover -s runtimes/tests -p 'test*.py'
# On an Apple-silicon Mac:
bash macos/scripts/package-app.sh
```

See [CLI contract](connector/CLI-CONTRACT.md), [native app](macos/README.md),
[runtime gates](runtimes/RELEASE-GATES.md), [security](SECURITY.md) and
[rename boundaries](MIGRATION.md). GitHub Actions are initially disabled and
the macOS job is manual-only after the owner approves CI use.

Public arbitrary-code execution, production tunnel dispatch, customer billing,
payouts, automatic private pooling and distributed MLX remain release blockers.
