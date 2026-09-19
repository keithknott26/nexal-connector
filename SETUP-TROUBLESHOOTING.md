# Nexal setup troubleshooting

## Shared dashboard and owner credentials

Open https://nexal-coordinator-dev.nexal.systems/ and authorize with the
Nexal owner credential provisioned for this coordinator. A Cloudflare API token
deploys infrastructure; it is not a Nexal dashboard or host credential.

The homepage previously returned raw `Owner bearer token required` JSON.
On September 19, 2026, Cloudflare inspection confirmed missing dashboard assets.
The dashboard and encrypted relay were deployed with migration 0006; the
homepage now returns HTML and displays the owner sign-in form.
Protected API endpoints still return 401 without the correct credential.

The UI keeps owner authorization only in the current tab's memory. Use
**Hosts > Enroll host** to generate a one-use invitation. Connector enrollment
consumes that invitation, not an owner token or Cloudflare token.

Enrollment now opens the dashboard's Hosts page in the Mac's default browser.
After owner sign-in, generate an invitation and return to Terminal to paste it.
If the browser does not open, use the printed address manually; the script
continues. `--no-browser` disables automatic opening. `--prepare-only` and
noninteractive runs never launch the browser. An interactive enrolled-profile
rerun opens Hosts for ID comparison without requesting a new invitation.

The owner reports successful dashboard login and an M4 host entry. Fresh
two-Mac enrollment and native M4-to-M2 delivery still need owner acceptance.
See the [deployment evidence](https://github.com/keithknott26/nexal-platform/blob/main/docs/SHARED-DEPLOYMENT-STATUS.md).

If raw JSON still appears at the homepage, reload and check the exact URL.
If it appears in Terminal, identify the command and endpoint without sharing
secrets. Do not assume every 401 has the same cause.

Do not paste owner/host tokens or invitations into chat, URLs or GitHub. Do not
reset profiles, reuse a localhost invitation on another coordinator, or expose
an anonymous local development server to the network.

## “An enrollment is already recorded”

This means the selected local configuration contains a host ID. The wrapper
does not verify the credential with the server. It now prints that ID, the
saved name, coordinator and config path; match the exact ID in the dashboard.
`--name` does not rename an existing identity. A requested name mismatch stops
instead of reporting a successful enrollment.

To enroll afresh without deleting or replacing credentials, use
`--enroll-platform --profile private-lan --name "M4 mini"` on the M4,
then the same options with `"M2 mini"` on the M2. Generate a separate one-use
invitation for each Mac. Both use the private-lan path on their **own disk**;
do not copy a config or Keychain credentials between Macs.

The new path is
`~/Library/Application Support/Nexal-Profiles/private-lan/config.json`.
Use that path with bundle-transfer `--config`. The menu-bar app is **not**
automatically switched to this separate test profile. Old hosts are not revoked,
and an old M4 entry may remain alongside the new one; compare host IDs.
If this named profile is already enrolled, use it or deliberately choose a
different unused profile name. Never delete an enrollment merely to clear a
prompt. Enrollment creates an identity but does not start heartbeats or jobs.

## “setup (donor)” during enrollment

Enrollment currently reuses `--donor --no-start` for prerequisite checks.
That heading does not make a receiver into a donor; no listener starts.
The final enrollment messages state what actually happened.

## Two profiles on one Mac

- `~/Library/Application Support/Nexal/config.json`: shared HTTPS coordinator.
- `~/Library/Application Support/Nexal-Local-Preview/config.json`: local preview.

Preserve both. Each coordinator has separate host identities; localhost on one
Mac is not the other Mac's service. Both Macs must enroll in the shared service
before using its relay.

For networking and bundle details see the
[LAN runbook](experiments/tcp-pager/MAC-TEST-RUNBOOK.md) and
[platform delivery guide](experiments/tcp-pager/PLATFORM-DELIVERY.md).
