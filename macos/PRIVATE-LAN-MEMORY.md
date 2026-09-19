# Private LAN workload memory

## Owner requirement

Memory contributed by the owner's locally paired Macs stays in the private LAN
pool. It must never become public marketplace capacity, even while idle.
Joining the marketplace is a separate decision for separately budgeted resources.

## Implemented safeguards

- `pool.NewPrivateMemoryAdmission` creates a private-only capacity authority:
  public work is rejected and its public capacity view is always zero.
- Mixed-resource authorities accept `PrivateMemoryBytes`, a non-borrowable
  reservation. Public admissions and public capacity reporting exclude it.
  Releasing private work does not make the reserved capacity public.
- `pool.PlanMLX` rejects public/marketplace scope and cloud fallback. Plans say
  `scope=private-lan`, `cloudFallbackAllowed=false`, `networkValidated=false`,
  and `executionValidated=false`.
- These changes do not increase the Go agent's reported physical RAM, send any
  private peer inventory to Cloudflare, open a listener, or provision cloud work.

## Not yet enabled on the owner's Macs

This is workload-level distributed-memory planning, not transparent macOS RAM
expansion or remote swap. It cannot remove the M2's memory-admission restriction
for the currently queued single-host Monte Carlo job.

The private pool APIs are not yet wired into the native lifecycle or an enabled
distributed MLX executor. No working two-Mac RAM pool is claimed. The following
must be completed before enabling that feature:

- Persist paired identities and revocations; require explicit owner pairing.
- Bind to an owner-selected local interface and exact paired peer addresses.
  A private IP alone does not prove the peer is physically local or the link fast.
- Measure peer RTT, throughput and stability on the actual M2/M4 path. Do not
  infer a fast interconnect from a shared Wi-Fi/mesh network name.
- Authenticate and encrypt the actual MLX data plane. The existing private
  object-transfer mTLS service is not an encrypted MLX collective transport
  and does not satisfy the strict post-quantum requirement.
- Account for private CPU, GPU/unified memory, loader peaks, KV caches,
  communication buffers and owner headroom in one local reservation authority.
- Restrict ranks, model shards, KV caches, temporary files and crash dumps to
  the private trust group. Fail closed on peer loss; never fall back to a public
  Mac, Cloudflare relay, RunPod, AWS or Azure.
- Test cancellation, sleep/wake, peer loss, pressure reclaim and recovery on both
  native Macs. Keep runtime execution disabled until those gates pass.

The protected-memory constructor and planner are library safeguards, not an
OS-enforced network sandbox. A future launcher must enforce the data-plane and
egress restrictions; callers cannot substitute a different admission authority.
