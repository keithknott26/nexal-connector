# neXal feature flags

The coordinator is the source of truth for commercial availability. It returns
these booleans in the connector status `features` object. Missing flags are
`false`; a client update must never silently turn on a paid or sensitive feature.

| Flag | Customer feature | Connector behavior |
| --- | --- | --- |
| `remote_ssh` | SSH client and key repository | Shows SSH only when TCP 22 is also observed on the peer's private tunnel address. Never enables Remote Login. |
| `remote_vnc` | Screen-sharing client | Shows VNC only when TCP 5900 or an authorized screen-sharing record is available. Never enables Screen Sharing. |
| `network_files` | neXal@home fileshare | Shows Files only for an authorized SMB record or TCP 445 on the private tunnel. |
| `wake_on_lan` | Wake a peer | Shows Wake only for a peer with a private tunnel identity. Existing coordinator authorization and rate limits still apply. |
| `chat` | Network chat | Reserved for the iOS/platform chat clients; connector defaults off. |
| `video_conferencing` | Realtime video calls | Reserved for the iOS/platform calling clients; connector defaults off. |

## Security contract

A feature flag controls presentation and product entitlement only. It is not an
access grant. The connector never enables macOS Remote Login, Screen Sharing, or
File Sharing without a separate, explicit administrator-authorized operation.
It never opens a public firewall port. Service discovery probes only the peer's
private mesh address and sends no application payload.

Temporary service enablement must be implemented as an expiring coordinator
grant bound to the requesting account, source peer, target peer, service, and
expiry. A privileged, signed helper may then request macOS authorization and
must restore the prior system state at expiry or network departure. Until that
complete lifecycle exists, neXal only advertises services the owner has already
enabled in System Settings.

The connector implements the grant verifier in `internal/remoteservice`. Grants
use an Ed25519 signature over a versioned canonical payload and are bound to the
authenticated source host, target host, service, issue time, expiry, and nonce.
They are limited to 24 hours. A verified grant only sets
`temporarilyAuthorized`; it never sets `observedEnabled`. Activation remains
blocked until the signed helper and administrator-consent UI are shipped.

Tunnel-status reports retain the legacy `services` list and add
`serviceCapabilities` entries with `service`, `observedEnabled`,
`temporarilyAuthorized`, `requiresAdminApproval`, and optional `expiresAt`.
This is backward-compatible with deployed coordinators and lets newer platform
and iOS clients distinguish availability from permission without guessing.

## Telemetry contract

Each peer reports cumulative `traffic.sentBytes`, `traffic.receivedBytes`,
`latencyMs`, `packetLossPercent`, and `path` (`direct`, `relay`, `cloud`, or
`unknown`). The macOS app derives rates between bounded five-second samples and
renders per-peer plus aggregate network series. Counter resets create a chart
gap; missing observations are never displayed as zero.

The tunnel-status write includes an additive `peers` array while retaining the
legacy best-path scalar fields. `controlPlaneTraffic` is reserved but omitted
until the authenticated HTTP transport provides real byte counters. Peer
traffic is never mislabeled as coordinator traffic.
