# nexal-vmhost

Tiny per-VM process (Virtualization.framework) that the Go connector launches
through launchd. Contract: header comment of `connector/internal/sandbox/vm.go`.

    nexal-vmhost run  --spec <file>      boot, serve control socket, exit when the guest stops
    nexal-vmhost stop --control <sock>   request ACPI shutdown, return immediately
    nexal-vmhost --version

Build and sign (ad-hoc, no paid account): `macos/scripts/build-vmhost.sh`.

## Spec fields consumed (JSON, names as in `sandbox.Spec`)

`sandboxId`, `hostname` (validated only), `cpus`, `memoryMB` (clamped to host/framework
limits), `diskPath`, `seedPath` (empty = none), `consoleLog`, `controlSocket`,
`guestSocket` (empty/absent = no guest channel), `desktop`. `keepAwake` is parsed and
ignored: no power assertion is ever taken.

Optional extensions the Go side does not send yet: `macAddress` (`aa:bb:..`),
`stopGraceSeconds` (default 30), `sharedDirs` (`[{"tag","path","readOnly"}]`, virtio-fs).

## Decisions where the contract was ambiguous

- **Guest channel**: `guest.go` says a second *virtio console port* (`/dev/hvc1`), not
  vsock, so that is what is built: a second virtio console device wired to a socketpair,
  relayed to the unix socket `guestSocket` (one client at a time; guest output with no
  client connected is dropped). There is no vsock device and no spec vsock port.
- **Status/pid file**: the contract names none, so none is written. `launchctl print`
  is the liveness source. The control socket answers `status` (`starting|running|stopping`).
- **Control protocol**: one line per connection: `stop` -> `ok`, `status` -> phase.
- **Exit codes**: 0 = guest powered off by itself or after a requested/forced stop;
  1 = failure (one line on stderr); 2 = usage error.
- **Persistence files** next to the disk: `<disk>.efivars` (UEFI NVRAM),
  `<disk>.machine-id`, `<disk>.mac` (used only when the spec has no `macAddress`).
  Deleting the disk should delete these too (the connector's cleanup does not know about
  them yet).
- **Path safety**: paths must be absolute, without `..`; the final component of disk,
  seed, shared dirs, console log and sockets must not be a symlink (parent directories may
  be, e.g. `/var`). Socket paths must fit in 103 bytes.
- **Desktop**: `desktop: true` adds a virtio GPU plus USB keyboard/pointer so the guest
  starts a display stack; nothing is shown on the host.

## Sleep / wake

No keep-awake assertion. When the Mac sleeps, macOS suspends this process and the guest
with it. Nothing is done on wake. The guest clock will be behind afterwards; if the
guest agent on hvc1 is enabled, it (or chrony/`systemd-timesyncd` in the guest) should
re-sync the clock. This is documented, not implemented.

## Not covered

Unit tests cover spec decoding/validation and resource clamping only; VM boot is
checked by hand with `SMOKE-TEST.md`. A LaunchAgent (gui/<uid> domain) is what the
connector uses; running Virtualization.framework from a system LaunchDaemon with no GUI
session is untested.
