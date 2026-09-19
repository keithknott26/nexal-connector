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

Successful owner login, new enrollment and native M4-to-M2 delivery still need
owner acceptance. Browser rendering alone does not establish those results.
See the [deployment evidence](https://github.com/keithknott26/nexal-platform/blob/main/docs/SHARED-DEPLOYMENT-STATUS.md).

If raw JSON still appears at the homepage, reload and check the exact URL.
If it appears in Terminal, identify the command and endpoint without sharing
secrets. Do not assume every 401 has the same cause.

Do not paste owner/host tokens or invitations into chat, URLs or GitHub. Do not
reset profiles, reuse a localhost invitation on another coordinator, or expose
an anonymous local development server to the network.

## “An enrollment is already recorded”

This means the selected local configuration contains a host ID. The wrapper
does not verify the credential with the server; check the shared dashboard
and authenticated connector behavior.

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
