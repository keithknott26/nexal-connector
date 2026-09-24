# Paid Time Machine destination (next release)

This connector contains the closed-by-default host contract for the paid Time
Machine destination. The coordinator supplies only non-secret desired state and
an active entitlement. The connector reports observed readiness; configuration
alone is never reported as healthy.

The intended data path is:

`Mac Time Machine client -> private neXal mesh -> SMB3 -> connector -> operator-provisioned JuiceFS mount -> customer R2 storage`

The connector requires SMB encryption/signing, no guest access, `vfs_fruit`, a
hard `fruit:time machine max size`, and allowlists only the coordinator-issued
private mesh CIDRs. It does not expose port 445 publicly. Usage billing must use
authoritative backend usage, not the configured quota or client-reported bytes.

## Required external provisioning

- An active paid entitlement and enabled development feature flag.
- A dedicated JuiceFS mount backed by R2. JuiceFS/R2 credentials are provisioned
  outside connector configuration and must never be returned by the coordinator.
- A privileged, signed helper to install/reconcile the SMB configuration and
  Bonjour advertisement. The unprivileged connector does not silently elevate.
- On Linux, Samba built with `vfs_fruit` and an isolated `nexal-timemachine`
  group. `internal/timemachine.RenderSambaShare` produces the managed stanza.
- On macOS, use Apple's native SMB service through `internal/lanshare`; do not
  ship Samba. The destination must be on an APFS-visible path. A JuiceFS/FUSE
  mount may not satisfy Apple's APFS requirement, so a local APFS sparsebundle
  cache/synchronization design needs a real compatibility test before release.

## Coordinator contract

- `GET /api/v2/devices/{hostID}/time-machine/config`
- `POST /api/v2/devices/{hostID}/time-machine/status`

Both use the existing host bearer credential. A missing, invalid, revoked, or
unentitled configuration fails closed. The status state is one of `disabled`,
`blocked`, `unsupported`, `action_required`, `configuring`, `degraded`, or
`ready`, with a stable `detailCode` suitable for the UI.

## Release blockers

Migration Assistant compatibility cannot be inferred from SMB reachability.
Before enabling production, test backup, browse, full restore, Migration
Assistant discovery, interrupted writes, quota exhaustion, R2 outage, and
concurrent clients on every supported macOS release. Apple does not document an
R2/​JuiceFS network filesystem as a supported shared Time Machine backing store.
Do not advertise Migration Assistant compatibility until that matrix passes.
