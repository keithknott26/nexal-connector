# neXal setup troubleshooting

## Host looks like the wrong Mac

Names such as `M4 mini` are labels, not hardware detection. Setup prints the
local chip, logical CPU count and physical RAM in bytes. Match its **saved host
ID**, not just its name, with Hosts. Enrollment reports `runtime.NumCPU()` and
macOS `hw.memsize`; changing `--name` does not rename a saved identity or update
its stored inventory. Do not overwrite RAM/CPU values to match an expected model.

If two cards have the same name, preserve both until their IDs are identified.
The shared dashboard now includes confirmed **Delete host** (migration 0007);
deletion revokes platform access, retains history and does not stop an existing
local pager. A deleted profile remains local and must not silently be reused as
a new identity.

Read-only inspection on September 19 identified `host_f01b747c-8f43-4e43-96f9-b49262ee6703`
as the earlier M2 saved identity: 8 cores, 8 GiB, recent heartbeats, mislabeled
`M4 mini`. The newer `host_8b8a0250-95dc-449a-b829-13d773b9f9e2` reports
12 cores and 24 GiB, matching the expected M4 inventory, with no heartbeat yet.
No identities were renamed or deleted. The M2 default profile already has an
active enrollment; its failed new `private-lan` enrollment is separate.

## Shared dashboard and owner credentials

Open https://nexal-coordinator-dev.nexal.systems/ and authorize with the
neXal owner credential provisioned for this coordinator. A Cloudflare API token
deploys infrastructure; it is not a neXal dashboard or host credential.

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

The owner reports successful dashboard login. Both saved identities were found;
native M4-to-M2 delivery still needs owner acceptance.
See the [deployment evidence](https://github.com/keithknott26/nexal-platform/blob/main/docs/SHARED-DEPLOYMENT-STATUS.md).

If raw JSON still appears at the homepage, reload and check the exact URL.
If it appears in Terminal, identify the command and endpoint without sharing
secrets. Do not assume every 401 has the same cause.

Do not paste owner/host tokens or invitations into chat, URLs or GitHub. Do not
reset profiles, reuse a localhost invitation on another coordinator, or expose
an anonymous local development server to the network.

## Enrollment HTTP 409 or wrong credential pasted

The owner token signs into the browser; it is **not** the enrollment invitation.
In the shared dashboard, use **Hosts > Enroll host > Generate invitation** and
copy the newly generated `enr_` code into Terminal. Each invitation is one-use
and expires after ten minutes. HTTP 409 means invalid, expired or already used.

Update the checkout and rerun the same enrollment command with the same
`--profile private-lan`; do not delete the configuration. The script now rejects
incorrect invitation formats locally without echoing or submitting the value.
Format validation does not establish validity: the coordinator still verifies
the invitation. If another error occurs, resolve that error before retrying.

## “coordinator request failed” on one Mac

This is a transport failure, **not** evidence of HTTP 409 or an invalid invitation.
Older builds discarded the reason and the shell then printed misleading 409
advice. Updated builds classify DNS, TLS certificate, timeout, refused/closed
connections and other network failures without printing secrets.

Update and rerun the same setup command. Before opening the browser or asking for
an invitation, the script performs a credential-free `/api/health` check through
the connector's own transport. A failed check stops without submitting a code.
After setup has built the helper, the check can be repeated independently:

```bash
"$HOME/Downloads/nexal-connector/experiments/tcp-pager/build/nexal-transfer" coordinator-check --config "$HOME/Library/Application Support/Nexal-Profiles/private-lan/config.json"
```

Browser access is not proof the connector route works: the connector deliberately
does not use automatic proxy settings. Check the category in the diagnostic;
review DNS/connectivity for `dns`, system clock/trust configuration for
`tls_certificate`, and reachability/outbound filtering for timeout or connection
errors. Do not disable TLS verification or indiscriminately disable firewalls.
If your network requires a proxy, report that requirement; this release has no
explicit proxy configuration.

A successful preflight does not guarantee a later enrollment response. If an
enrollment request loses its response, server-side success is ambiguous: review
Hosts before retrying. There is no automatic retry or transactional recovery,
and a fresh invitation alone does not recover a lost host credential.

## Why the receiver needs its own enrollment

The private platform relay delivers only between enrolled sender and receiver
identities. The M2 must have its own host credential to request/download the
encrypted bundle. The donor's client bundle contains separate short-lived pager
connection credentials; it cannot enroll the receiver or replace that identity.
Never copy the M4's platform configuration or host credentials to the M2.
The desired phone-login/QR flow will automate this authorization but is not yet
integrated. Direct manual bundle delivery is a separate lab path, not automatic
platform enrollment.

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
