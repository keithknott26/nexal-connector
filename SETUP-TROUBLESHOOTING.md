# Nexal setup troubleshooting

## Shared homepage says “Owner bearer token required”

On September 19, 2026, a credential-free check of
`https://nexal-coordinator-dev.nexal.systems/` returned HTTP 401 and JSON:

```json
{"error":{"code":"unauthorized","message":"Owner bearer token required."}}
```

Its `/api/health` endpoint previously returned HTTP 200 with production mode.
A health response does not establish that dashboard assets or the relay are
deployed. This is a reproduced homepage problem, not evidence that the Mac
needs a new network interface or replacement enrollment.

The current repository serves a static dashboard through the `ASSETS` binding.
The dashboard then asks for owner authorization, retained only in its tab's
memory. Owner API calls such as invitation creation require that credential;
host enrollment consumes a one-use invitation instead, not an owner token.

If the owner-auth JSON appeared in Terminal rather than at the homepage,
record the command and endpoint (without secrets) for separate investigation.
Do not assume every 401 has the same cause.

### Required deployment work

- Inspect the actual live Worker version, hostname route, D1 binding and
  static-assets configuration. The root response alone does not prove which
  deployment setting or version is wrong.
- Build and deploy the dashboard with the appropriate Worker asset binding,
  retaining owner authentication on protected API/MCP routes.
- Before enabling relay code, apply its required migrations, including 0006,
  to the intended database without overwriting the owner's staging config.
- Verify `/` serves HTML, its JS/CSS load, the owner login works, unauthenticated
  owner APIs still reject requests, and invitation/enrollment succeeds.

No remote deployment, migration, token rotation or DNS change was performed
while investigating this issue. The connected Cloudflare tools available in
this session do not expose Workers deployment; no separate Cloudflare API
credential is available. The owner can authorize appropriately scoped deployment
access securely or run a reviewed deployment from their configured checkout.

Do not paste owner/host tokens or invitations into chat, URLs or GitHub. Do not
reset profiles, copy a localhost invitation to another coordinator, or make an
anonymous local development server accessible to the network.

## “An enrollment is already recorded”

This means the selected local configuration contains a host ID. The wrapper
does not verify the credential with the server; check the shared dashboard
and authenticated connector behavior once that service is ready.

## “setup (donor)” during enrollment

Enrollment currently reuses `--donor --no-start` for prerequisite checks.
That internal heading does not make a receiver into a donor. No listener starts
during that check. The final enrollment messages state what actually happened.

## Two profiles on one Mac

- `~/Library/Application Support/Nexal/config.json`: shared HTTPS coordinator.
- `~/Library/Application Support/Nexal-Local-Preview/config.json`: local preview.

Preserve both. The same host can have separate identities in separate
coordinators; localhost on one Mac is not the other Mac's service.

For networking and bundle details see the
[LAN runbook](experiments/tcp-pager/MAC-TEST-RUNBOOK.md) and
[platform delivery guide](experiments/tcp-pager/PLATFORM-DELIVERY.md).
