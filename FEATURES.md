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

Security scope is tripwire, decoy and honeypot only: neXal does not provide
antivirus, malware or file scanning, or process inspection (the YARA-X scanner
and `nexal security` commands were removed). The opt-in honeypot
(`nexal honeypot --action status|enable|disable`) opens fake SSH 2222, Telnet
2323, RDP 3389, SMB 4445, VNC 5909 and HTTP 8081 services bound only to this
Mac's neXal network and private-LAN IPv4 addresses, never a public address. Any
non-loopback connection gets a banner, is held at most five seconds with at most
512 bytes read and discarded, and produces a `network_alert` event
(`nexal_honeypot_<service>`; no address leaves the Mac). Alerts are limited to one
per source and port per hour and 20 per hour, with a 20-event outbox; the source
address is kept locally in the last 20 connections only.

## Telemetry contract

Each peer reports cumulative `traffic.sentBytes`, `traffic.receivedBytes`,
`latencyMs`, `packetLossPercent`, and `path` (`direct`, `relay`, `cloud`, or
`unknown`). The macOS app derives rates between bounded five-second samples and
renders per-peer plus aggregate network series. Counter resets create a chart
gap; missing observations are never displayed as zero.
