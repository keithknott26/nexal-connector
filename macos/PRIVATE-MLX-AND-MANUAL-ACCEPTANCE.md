# Private MLX development and manual acceptance

Status: engineering preview. No public launch, paid provisioning, DNS changes,
or changes to the owner's Macs are performed by this source update.

## Accept jobs now

Matching platform/connector `COMPATIBILITY` v2 and local D1 migration
`0005_manual_private_acceptance.sql` are required. Update both repositories and
rerun the existing native setup before launching the new app.

The menu-bar button asks the already-running Go connector to accept private,
zero-cost CPU jobs for ten minutes without waiting for owner inactivity. It
does not start a competing daemon. Actual owner activity is still reported.
Repeated clicks during the window do not extend it. Pause/cancel, policy change,
restart or expiry remove this permission. Memory pressure, unknown/stale
telemetry, leases, revocations, authentication and all production gates remain.
The app displays the first execution blocker rather than silently promising work.

The M2's previous low-memory report is independent of the idle timer. The button
cannot make that machine eligible unless real available memory meets its reserve
and workload requirements. It does not enable MLX jobs in the coordinator.

## Private memory policy

All memory assigned to the local private pool stays private, including when idle.
The pool admission library excludes `PrivateMemoryBytes` from public admission
and export. Its private-only constructor rejects public work entirely.
The MLX planner rejects public scopes and cloud fallback. Plans explicitly remain
network-unvalidated and execution-unvalidated.

These are library safeguards. The current native daemon is not yet a functioning
distributed-memory service, and pooled peer memory is not added to physical RAM
inventory. The production marketplace is still disabled.

## MLX and hardware boundaries

The owner's base M4 mini has Thunderbolt 4. The M4 Pro variant has Thunderbolt 5
([Apple specifications](https://support.apple.com/en-us/121555)).
MLX documents TCP ring communication over network links, including Ethernet and
Thunderbolt; JACCL uses Thunderbolt RDMA on supported Thunderbolt 5 Macs, requires
macOS 26.2 or later and a fully connected topology
([MLX distributed documentation](https://ml-explore.github.io/mlx/build/html/usage/distributed.html)).
Changing a Recovery setting does not add Thunderbolt 5 hardware.

Do not present distributed MLX as transparent system RAM expansion. MLX is a
cooperating application runtime. Apple's archived VM documentation describes
OS-managed paging and file-backed mappings, not a supported system-wide remote
RAM plug-in ([Apple VM documentation](https://developer.apple.com/library/archive/documentation/Performance/Conceptual/ManagingMemory/Articles/AboutMemory.html)).
That absence is not proof of theoretical impossibility, but no supported,
production-safe implementation of arbitrary-app remote RAM is established here.
We will not patch the kernel, disable SIP/Gatekeeper, or change swap settings.

## Implementation sequence

- Supervised local MLX: fixed command, pinned local code/model/environment,
  bounded output, cancellation, deadlines and validated result identity.
  Implemented as `RunLocalInference` and `cmd/nexal-mlx-job` in the connector's
  standalone `runtimes/bridge` module. Go helper-process tests/race/vet and
  Darwin ARM64 cross-compilation passed, not actual Python/MLX execution.
  A per-config lock prevents concurrent launches against that config, not
  cross-config overcommitment. No arbitrary user-supplied shell commands or
  model downloads. The runner consumes an existing admission snapshot and
  is not yet integrated with authoritative pool reservations or native UI.
- Native acceptance: approved dependency lock, verified small model, actual
  M2/M4 Metal inference, memory pressure, cancellation and sleep/wake testing.
- Private multi-Mac jobs: persistent identity pairing, interface/route pinning,
  measured link quality, shared reservation authority and rank-wide cancellation.
  Raw MLX TCP/RDMA transport is not automatically authenticated, encrypted or
  post-quantum because a Cloudflare tunnel exists elsewhere.
- Application integration: an authenticated local inference endpoint and
  explicitly supported app adapters. Apps using that endpoint can have work
  placed on the private cluster without choosing a rank themselves. This is
  application-level transparency, not larger RAM shown by Activity Monitor.
- Remote object/tensor APIs: optional future SDK for cooperating applications,
  with explicit failure handling. No automatic public fallback or remote swap.

Do not expose confidential distributed jobs until the actual collective data
plane satisfies the owner's strict cryptographic and private-routing requirements.
Do not downgrade those requirements to get an experimental demo working.
