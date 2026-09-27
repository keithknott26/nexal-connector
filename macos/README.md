# neXal-Connector for macOS

The platform repository now includes double-click `Setup neXal.command` and
`Start neXal.command` launchers. See its `docs/MAC-SETUP.md` for the recommended
native setup. Packaging builds into a separate staging directory and preserves
the previous complete app on ordinary build failure; quit the app before updates.
This is still an unsigned source-build workflow, not a notarized installer.

The current enrollment UI uses an HTTPS Universal-Link QR plus a separately
generated eight-character manual code. It persists successful account/network
membership through the Go connector and Keychain. The status surface accepts
vendor-neutral per-peer authentication, direct/relay path, traffic, latency and
post-quantum evidence; it says unavailable when no privileged mesh provider is
installed rather than simulating connectivity.

For a local development artifact run `bash scripts/build-local-dmg.sh`. Install
it explicitly with `bash scripts/install-local-dmg.sh --confirm-local-unsigned`.
This is recoverable (an existing app is moved to Trash) and is not a substitute
for the signed, notarized release workflow.

Native SwiftUI menu-bar shell for **macOS 14 or newer**. Swift owns presentation
and process lifecycle only; the **Go connector** owns enrollment, Keychain
credentials, owner policies, limits, telemetry, heartbeat, pause/cancellation,
resource admission, tunnel verification and workload execution. There is no
second scheduler, direct coordinator client, browser wrapper or credential store.

**Status:** owner-supplied M4 output now confirms native compilation, seven
XCTest cases and initial app packaging for connector `76345d9`. Interactive UI,
Keychain and runtime hardware acceptance are not yet complete; see
[`../MAC-ACCEPTANCE.md`](../MAC-ACCEPTANCE.md). No Developer ID signed/notarized
binary is included. This is not production-ready or M2-validated.

## What the shell does

- Explicitly select the bundled `Contents/Helpers/nexal`, or the exact installed
  `~/Library/Application Support/Nexal/bin/nexal`. Never searches `PATH`.
- Check regular-file permissions, reject symlinks, and remember the SHA-256 of
  the owner-selected executable. A changed executable requires selection again.
  A hash pin is **not** publisher authentication; verify the code signature.
- Initialize a private, paused Go configuration with coordinator URL, host name,
  chosen memory allowance and owner reserve. Existing config is never overwritten.
- Pass a one-use code to `nexal enroll --code-stdin` through a pipe, not arguments,
  environment, preferences or logs. The actual code field is cleared afterward.
  Successful enrollment shows a fixed, display-only 24-dot mask and an
  **Enrolled** label, not the consumed code. **Use another code** explicitly
  reopens entry and requires fresh consent. Confirmation is scoped to the chosen
  configuration; authenticated local status can restore it after restarting.
  It is enrollment feedback, not proof of coordinator connectivity or job readiness.
- Start `nexal run`, or attach to an existing local connector; display Go status,
  transport and real/synthetic/unknown telemetry without inventing readiness.
- Map the private resource toggle to `resume` / `pause`, with a separate explicit
  **Pause and cancel work** action. Only confirmed status changes the toggle.
- Offer a prominent **Accept jobs now** button in Owner controls for an enrolled
  local-preview connector. This invokes `nexal accept-jobs` through the same
  authenticated CLI, starts or attaches to the existing daemon, and does not
  launch a separate executor. Its ten-minute permission allows zero-cost private
  CPU work while the owner is active, without bypassing memory or lease checks.
  Confirmed status changes the label to **Accepting private jobs**, shows the
  local expiry time and disables repeat clicks. Failure to confirm does not
  display success. Pause cancels work and clears permission.
- Keep cloud/marketplace contribution visibly gated. Private membership never
  constitutes public consent; this build does not simulate an unsupported public
  toggle, configure folder sharing, advertise payouts, or buy cloud compute.

Starting the app enables only ordinary `run`. It cannot silently opt into
development pull transport or synthetic telemetry. **Accept jobs now** is the
separate explicit permission for development private execution. Production
dispatch remains closed in the Go core. A resumed policy or acceptance window
is not evidence of running work.

neXal revision: an explicit **Use local development preview** option now creates
the fixed numeric-loopback configuration in a separate `Nexal-Local-Preview`
directory. Its file credentials are disclosed in the UI. It does not enable
synthetic telemetry or, by itself, development pull execution. The new native UI
tests are provided but have not been run in the Linux authoring environment.
See [ACCEPT-JOBS-UI-ACCEPTANCE.md](ACCEPT-JOBS-UI-ACCEPTANCE.md) before marking
the updated button accepted on a Mac.

## Exact Go CLI integration (v1)

The authoritative interface is [`../connector/CLI-CONTRACT.md`](../connector/CLI-CONTRACT.md).
Every invocation uses Foundation `Process.executableURL` and a separate argument
array, with `--config` appended after the command flags:

```text
nexal init --coordinator URL --name NAME --memory-limit-mib N --reserve-memory-mib N --config ABSOLUTE_CONFIG
nexal enroll --code-stdin --config ABSOLUTE_CONFIG       # one-use code + newline on stdin
nexal run --config ABSOLUTE_CONFIG
nexal status --config ABSOLUTE_CONFIG
nexal pause --config ABSOLUTE_CONFIG
nexal resume --config ABSOLUTE_CONFIG
nexal accept-jobs --config ABSOLUTE_CONFIG             # explicit local-preview permission
```

`ABSOLUTE_CONFIG` is `~/Library/Application Support/Nexal/config.json`, expanded
without a shell. `status` JSON must contain boolean `paused`; the app reads
`hostId`, `version`, `marketplaceEnabled`, `mode`, `transport`,
`productionDispatchVerified`, `activeAttempt`, and telemetry if present.
Unknown fields are ignored; a missing pause field fails closed. The core's
current status does not emit a name; the optional name decoder does not assume it.
Version these adapters and the bundled Go binary together if the core evolves.

Short CLI commands run off the main actor with bounded 64 KiB output, separately
drained stderr, and a 20-second deadline followed by termination escalation.
The UI never renders raw stderr. Selected executable changes are checked again
before every launch. This is not isolation against a malicious same-user process.
The long-lived child's output is discarded rather than logged with credentials.

## Build on an Apple-silicon Mac

Install Apple's Xcode (including command-line tools) and the Go version required
by `../connector/go.mod`. Keep Gatekeeper and SIP enabled. No root launch,
download-and-execute installer, login daemon or firewall change is required.

From the repository root:

```bash
xcode-select -p
xcodebuild -version
swift --version
go version

# Native Swift Package: compile and execute XCTest on macOS, not Linux.
cd macos
export MACOSX_DEPLOYMENT_TARGET=14.0
swift build -c release --arch arm64
swift test --arch arm64

# Xcode GUI workflow: open Package.swift; choose the NexalMac scheme and My Mac.
open -a Xcode Package.swift
# Xcode CLI alternative, after the scheme is visible:
xcodebuild -scheme NexalMac -destination 'platform=macOS,arch=arm64' \
  -derivedDataPath build/Xcode build

# Assemble a real .app including the Go arm64 connector; remains unsigned.
bash scripts/package-app.sh
```

The script builds Go with `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64`, assembles an
`LSUIElement` app, and runs Swift tests. The deployment target of the shell does
not certify a Python/Metal environment or every feature on macOS 14. MLX has
separate target-version, dependency and hardware gates.

## Signing and notarization (release operator)

Use your own Apple Developer team, a unique owned bundle identifier in
`Resources/Info.plist`, a Developer ID Application certificate, and an incremented
build number. Do not ship using another team's identity. These exact commands
are **instructions to run on macOS**, not a claim they were run here.

```bash
# Run from macos after packaging.
APP="$PWD/build/neXal-Connector.app"
IDENTITY='Developer ID Application: YOUR ORGANIZATION (YOURTEAMID)'
security find-identity -v -p codesigning

# Sign inside-out. Do not use --deep to sign, disable library validation,
# request get-task-allow, disable Gatekeeper/SIP, or clear quarantine.
codesign --force --options runtime --timestamp --sign "$IDENTITY" \
  "$APP/Contents/Helpers/nexal"
codesign --force --options runtime --timestamp --sign "$IDENTITY" "$APP"
codesign --verify --deep --strict --verbose=2 "$APP"
codesign -dv --verbose=4 "$APP"

# Store notary credentials interactively in the login Keychain, not shell history.
xcrun notarytool store-credentials Nexal-notary
ditto -c -k --keepParent "$APP" "$PWD/build/Nexal-Connector-submission.zip"
xcrun notarytool submit "$PWD/build/Nexal-Connector-submission.zip" \
  --keychain-profile Nexal-notary --wait
# Continue only if Accepted. Inspect rejected submissions with notarytool log.
xcrun stapler staple "$APP"
xcrun stapler validate "$APP"
spctl --assess --type execute --verbose=4 "$APP"
ditto -c -k --keepParent "$APP" "$PWD/build/Nexal-Connector-notarized.zip"
shasum -a 256 "$PWD/build/Nexal-Connector-notarized.zip"
open "$APP"
```

No App Sandbox entitlement is asserted: the current Go child/Keychain/process
design needs a real entitlement and isolation review before an App Store build.
Hardened runtime and notarization are distribution checks, not a malicious-job
sandbox or proof of Metal access. Test the notarized download on another Mac.

For an explicitly reviewed standalone connector installation (instead of bundle):

```bash
install -d -m 700 "$HOME/Library/Application Support/Nexal/bin"
install -m 700 "$APP/Contents/Helpers/nexal" \
  "$HOME/Library/Application Support/Nexal/bin/nexal"
codesign --verify --strict --verbose=2 \
  "$HOME/Library/Application Support/Nexal/bin/nexal"
```

Then choose that executable in the app. No code is installed by the app itself.

## Required Mac acceptance tests

1. Run `swift test` and the connector's `go test ./...` on the target Mac.
2. Build, sign and notarize; record exact chip, macOS build, Xcode/Swift/Go versions.
3. Verify setup creates private paused config and Keychain items, not plaintext
   secrets. Check command listings contain no enrollment code. Verify code expiry,
   one-use replay rejection, wrong coordinator, offline enrollment and Keychain lock.
4. Start, observe real telemetry, refresh, pause, resume, and stop. Observe Go
   state independently, not only UI. Pause must cancel a running approved attempt.
5. Test app-launched versus independently started connector, duplicate start,
   crash/restart, laptop sleep, owner activity and network loss.
6. Replace/tamper with selected executable: UI must refuse it until reselected.
   Test symlink and world-writable candidates and oversized/hung CLI output.
7. Verify private toggle never enables marketplace, claims PQ attestation,
   mounts storage, configures SSH or initiates paid fallback.
8. VoiceOver, keyboard navigation, contrast, menu-bar window sizing and Keychain
   authorization prompts need visual/native QA. No screenshots are simulated.

Quitting sends SIGTERM only to the child launched by this app; independently
started services remain running. The core's cancellation and descendant cleanup
must be validated on macOS. Launch at login is enabled by default on first launch through macOS Login Items.
Use the connector’s gear menu or right-click its menu-bar icon for **Settings…**
and **About neXal Systems Connector**. Settings includes a **Start neXal Systems
Connector at login** checkbox; disabling it is preserved across relaunches.
If macOS requires approval, Settings provides a link to Login Items. When enabled, the two-second
splash closes directly to the menu bar without opening a main window. Reopening
the app from Finder or choosing **Open Connector** opens the main window.
Settings has **General** and **Security** tabs. General also includes persistent
preferences for showing the splash, playing the pairing sound, flashing the
menu-bar alert icon, and the activity graph range (5 or 30 minutes). The first
three default to on, preserving existing behavior; the graph defaults to 30
minutes. Changing the range in either Settings or the network panel updates
both. Turning off flashing retains the static red alert icon.

Security contains the existing integrity canary and file-scanning/code-style
controls for linked Macs. Opening Settings does not enable either feature.
Automatic signed updates are not installed.

File Sharing and Screen Sharing are consent-preserving: the UI can open System
Settings for the owner, but never enables either service or changes firewall
rules. Finder and VNC actions appear only for policy-authorized peers with a
reported active macOS service and a stable private
`<short-id>.mesh.nexal.systems` hostname. Discovery status distinguishes
Wide-Area Bonjour, the optional site gateway, and the authenticated bridge; it
does not claim that multicast or a shared Ethernet segment spans sites.
