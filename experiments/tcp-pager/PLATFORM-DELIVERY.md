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
No remote deployment or native two-Mac acceptance has been performed.

From this module on the M2:

```sh
bash scripts/platform-bundle-macos.sh receive \
  --config "/ABSOLUTE/M2/config.json" --from-host "M4_HOST_ID"
```

Keep it open. Copy the PUBLIC transfer ID and receiver key fingerprint to a
second terminal on the M4, then send the current donor's client folder:

```sh
bash scripts/platform-bundle-macos.sh send \
  --config "/ABSOLUTE/M4/config.json" \
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
