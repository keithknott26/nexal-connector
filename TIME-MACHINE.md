# Paid Time Machine destination (next release)

## Gateway mode (current direction, development)

Macs are backup **clients** of an operator-run storage gateway reached directly
over the NetBird mesh. The gateway itself is operator infrastructure and is
not part of this repository.
`nexal time-machine` reports readiness and `nexal time-machine -connect` adds
the gateway with `tmutil setdestination -a -p` after checking that the gateway
name resolves only to mesh (100.64.0.0/10) addresses. The coordinator config
document carries `role: "client"` and a `destination`; the SMB password comes
only from `POST …/time-machine/credentials`. The connector-hosted design below
is retained for reference and remains fail-closed.

### How the SMB password reaches tmutil

The password never appears in any process's argv (visible to every local user
via `ps`) or in sudo's command log. The connector runs:

    sudo [-n] -- /usr/bin/script -q /dev/null \
      /usr/bin/tmutil setdestination -a -p smb://<user>@<host>/<share>

- The URL is password-free (user and share percent-escaped). `tmutil(8)`
  documents that with `-p` it asks "at a non-echoing interactive prompt"
  instead of taking `user:pass` in the URL, because arguments are visible in `ps`.
- A non-echoing prompt reads the controlling terminal (`/dev/tty`, as
  `readpassphrase(3)`/`getpass(3)` do), not a stdin pipe. `script(1)` gives
  tmutil a pseudo-terminal as its controlling tty (`login_tty`) and forwards
  the connector's stdin pipe into it.
- The connector writes `password\n` only after tmutil has printed its prompt
  (echo is already off then, and input sent earlier could be flushed or echoed),
  then EOF. Captured pty output is redacted before it is ever printed.
- sudo asks for the administrator password on the user's own `/dev/tty`; `-S`
  is never used, so sudo cannot consume the piped SMB password. With no
  controlling terminal (e.g. launched from the GUI app) `sudo -n` is used and
  fails immediately with "run `nexal time-machine -connect` in Terminal" if a
  password would be needed. The whole step has a 3-minute timeout.
- Residual: a sudoers `log_input` policy would record the piped password in
  sudo's I/O log; root can always read it. Terminal needs Full Disk Access.

Manual check on a Mac (in Terminal, while running `-connect`, in a second
window): `ps -axww -o pid,command | grep -E 'tmutil|script|sudo'` must show
no password, and `log show --last 5m --predicate 'process == "sudo"'` must
show only the password-free URL.


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
- A dedicated JuiceFS mount backed by R2. Short-lived R2 credentials come from
  the separate host-authenticated credential endpoint and are never embedded in
  configuration, status, logs, command arguments, or the native UI.
- A tenant-scoped JuiceFS metadata service. The current Worker contract returns
  temporary R2/S3 object credentials but no JuiceFS metadata URL or credential;
  R2 is object storage, not the metadata engine JuiceFS requires. Until that
  contract exists, `nexal time-machine` reports
  `juicefs_metadata_unconfigured`, does not mint the R2 credential, and does not
  create or advertise a share.
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
- `POST /api/v2/devices/{hostID}/time-machine/credentials`
- `POST /api/v2/devices/{hostID}/time-machine/usage`

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
