# Nexal platform delivery instead of AirDrop

The platform can relay an encrypted pager client bundle between two explicitly
selected enrolled Macs. The M2 writes the received files to a new private
application-support folder and automatically starts the existing receiver script
with that path; `~/Downloads/client` is not needed.

Full deployment, commands, security model and validation:
[Nexal private pager bundle relay](https://github.com/keithknott26/nexal-platform/blob/main/docs/PRIVATE-BUNDLE-RELAY.md).

Prerequisites: coordinator code and migration 0006 installed, both Macs enrolled
in the same reachable coordinator, valid host credentials, M4 donor still running.
Two separate loopback coordinators do not constitute a shared service.
The shared-dev coordinator and migration 0006 were deployed September 19, 2026.
Native two-Mac acceptance remains pending; deployment does not enroll either Mac.

## Reusable platform enrollment

From the connector repository root, on either Mac:

```sh
bash experiments/tcp-pager/scripts/setup-lan-macos.sh \
  --enroll-platform --profile private-lan --name "M4 mini"
```

For the M2 use `--name "M2 mini"`. The default shared coordinator is
`https://nexal-coordinator-dev.nexal.systems`. The named profile above uses
`~/Library/Application Support/Nexal-Profiles/private-lan/config.json` on each
Mac separately, preserving the original default
`~/Library/Application Support/Nexal/config.json`. Without `--profile`, the
original default remains selected. Use `--coordinator HTTPS_ORIGIN`
and/or `--config ABSOLUTE_PATH` to select a different deployment explicitly.
Do not use the localhost preview profile for a cross-Mac relay.

This mode checks prerequisites using the existing consent-based installer,
builds the CLI, and initializes a private, paused profile only if none exists.
After the build it prompts for a one-use invitation with terminal echo disabled.
Generate that invitation in the shared dashboard under **Hosts > Enroll host**.
No invitation is accepted as an argument, environment variable or saved file.
Keychain may request approval. Enrollment does not start the agent or pager,
enable jobs/public sharing, deploy the coordinator, or apply remote migrations.

Use `--prepare-only` to stop before the invitation prompt. Rerun without it when
ready; reruns preserve configuration, resource policy and credentials. A saved
host ID skips re-enrollment but does **not** prove that credentials are still
valid. Saved identity details are printed; an explicitly requested name mismatch
stops without changing that identity. A different saved coordinator is rejected instead of silently replacing
the profile. Interrupted or rejected invitations can be retried; an ambiguous
server-side success may require a fresh invitation and dashboard review.
The wrapper does not provide transactional recovery for the underlying enrollment
API if connectivity or Keychain persistence fails after the server commits.

Run from an interactive Terminal. If invoking inside a here-document, append
`</dev/tty` to give the hidden prompt a terminal. No reboot or login task is
installed. The underlying standalone `--donor` and `--receiver` modes are unchanged.

Validation: 12 setup tests plus 11 enrollment orchestration tests,
including hidden-input pseudo-terminal success/failure/retry, safe reruns,
coordinator mismatch, no-terminal refusal, build failure and symlink rejection.
These are Linux mocks, not native macOS/Keychain acceptance.

## Deliver the pager bundle

From this module on the M2:

```sh
bash scripts/platform-bundle-macos.sh receive \
  --config "$HOME/Library/Application Support/Nexal-Profiles/private-lan/config.json" --from-host "M4_HOST_ID"
```

Keep it open. Copy the PUBLIC transfer ID and receiver key fingerprint to a
second terminal on the M4, then send the current donor's client folder:

```sh
bash scripts/platform-bundle-macos.sh send \
  --config "$HOME/Library/Application Support/Nexal-Profiles/private-lan/config.json" \
  --transfer "TRANSFER_ID" --receiver-key-sha256 "RECEIVER_PUBLIC_KEY_SHA256" \
  --bundle "/ABSOLUTE/DONOR/STATE/client"
```

No folder transfer via AirDrop is required. The sender fingerprint check and
the existing donor CA fingerprint prompt are intentional security checks; do
not bypass them. Only ephemeral pager keys are transferred, never the donor's
server key or Nexal enrollment credentials. Only the four expected files are
accepted; bundle contents are not scripts and are never executed.

There is no background file scraping, automatic dashboard action or direct
operating-system memory expansion. This explicit opt-in bridge is separate from
the normal connector agent, so it can run while the menu-bar agent owns its
configuration lock; it reads config/Keychain but does not mutate enrollment.
