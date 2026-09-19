# Nexal bounded TCP-backed CPU-paging prototype

This is a separate research module, not a feature in the installed Nexal Connector.
It contains a tested Go page transport/cache and a Mac-only C Hypervisor helper.
The owner reported successful native build, signature verification and CPU-fault
loopback acceptance on September 19, 2026; see [VALIDATION.md](VALIDATION.md).
Paging between two separate Macs remains untested.

**New scripted workflow:** read [the Mac test runbook](MAC-TEST-RUNBOOK.md).
`bash scripts/accept-memory-macos.sh` runs the baseline tests, a real
CFAllocatorCreate scope diagnostic, and before/during/after host RAM observations.
The OS-visible RAM requirement intentionally remains unimplemented; a successful
lower-level run ends with exit 3 rather than falsely claiming that requirement
passed. New native observation/CF code still needs owner acceptance.
The guided LAN scripts automate setup, short-lived credentials and repeated fresh
test sessions; they do not integrate with the production connector.

## What it does

- Uses 16 KiB pages, 64 pages by default (1 MiB logical dataset), and a four-page
  resident payload cache (64 KiB).
- Caps donor payload at 256 pages (4 MiB), cache at 16 pages, and donor sessions
  at one active TCP connection.
- Uses TLS 1.3 mutual authentication with strict X25519+ML-KEM-768 key agreement.
  The test rejects peers that offer only classical X25519. Certificate signatures
  remain Ed25519: this is hybrid key agreement, not fully post-quantum authentication.
- Tracks per-page versions and SHA-256 digests on the receiver; fails closed on
  corrupted/stale pages, disconnects and ambiguous write acknowledgements.
- Reserves donor payload upfront, uses a private numeric IP only, and never calls
  the coordinator, Cloudflare, billing, public marketplace, MLX or a cloud provider.
- Keeps keys off command lines and logs, generates 24-hour experimental credentials,
  and refuses to overwrite an existing credential directory.

The payload bounds are not process-RSS limits: Go, TLS, metadata, executable code
and temporary page buffers use additional memory. Donor RAM is volatile; there is
no replication, persistence, reconnect, resumable workload or automatic failover.

## Two different tests, honestly labeled

| Mode | What actually happens | What it does not prove |
|---|---|---|
| Portable | Go explicitly accesses a bounded page cache; misses and dirty evictions go through TCP/TLS. Every verification page is refetched after discarding the first cache. | Not CPU-fault-driven; no VM or OS memory expansion. |
| Native HVF | A tiny fixed AArch64 guest writes and checks every 64-bit word. Unmapped data accesses cause Hypervisor exits; C requests pages through the Go broker, maps actual local backing and retries the original instruction. | Not a macOS guest; no native application transparency, GPU support, firmware modification or 100 GB allocation. |

`selftest` hosts the donor on loopback, in the same Go process. That deliberately
tests paging machinery without requiring a second machine; it does not increase
the host's available RAM. The two-machine instructions below use the same protocol.

## Run native acceptance on the M4 or M2

Use a clean checkout of the latest private connector. Do not reset or overwrite
your existing dirty checkout or acceptance worktree.

From this directory:

```sh
bash scripts/accept-macos.sh
```

The script:

1. Requires Apple-silicon macOS, Xcode command-line tools and Go 1.26 or newer.
   It selects `/opt/homebrew/opt/go@1.26/bin/go` when present, otherwise your
   existing `go`; automatic toolchain downloads are disabled.
2. Runs Go race tests and vet, then builds the Go tool and C helper.
3. Ad-hoc signs the locally built helper with only the public
   `com.apple.security.hypervisor` entitlement.
4. Runs the portable test, then the actual native CPU-fault test.
5. Writes the two JSON outcomes under ignored `build/`.

No Homebrew installations, sudo, Recovery changes, SIP/AMFI changes, private
entitlements, firmware changes, production enrollment or large VM are requested.
Ad-hoc signing is for this local development experiment, not notarized distribution.
If a compiler, entitlement or Hypervisor operation fails, stop and share its error;
do not disable security to get past it.

Native result reported by the owner after a successful real Mac run:

```json
{
  "mode": "native HVF CPU-fault pager",
  "verified": true,
  "logicalBytes": 1048576,
  "cachePayloadLimitBytes": 65536,
  "verifiedBytes": 1048576,
  "cache": {
    "faults": 128,
    "evictions": 120,
    "peakResidentPages": 4
  },
  "transport": {
    "gets": 128,
    "puts": 64,
    "pageBytesReceived": 2097152,
    "pageBytesSent": 1048576
  },
  "nativeHVFExecuted": true,
  "macOSGuestBooted": false,
  "hostRAMExpanded": false,
  "gpuMemoryExpanded": false
}
```

This is transcribed owner-reported loopback evidence, not an independently
executed agent test or a two-Mac result. The portable test correctly reported
`nativeHVFExecuted: false`. Exact hardware/OS/revision were not included.

## Use a donor on another private-LAN computer

Prefer the guided donor/receiver scripts in [the runbook](MAC-TEST-RUNBOOK.md).
The low-level commands below remain supported for a single workload; unlike the
separate lab fixture, this original donor does NOT reset between connections.

First pass native loopback acceptance. Then build the Go tool on each machine;
only the receiving Mac needs the native helper.

On the donor, from this module directory:

```sh
go build -o build/nexal-pager ./cmd/nexal-pager
./build/nexal-pager init --dir "$HOME/nexal-pager-test-keys"
```

This creates private `donor/` and `client/` directories. Transfer ONLY the `client/`
directory to the receiving Mac through a trusted authenticated channel, retaining
0700 directory and 0600 file permissions; keep these credentials out of GitHub,
screenshots and chat. The CA private key is not persisted.

Start the donor with its actual numeric private LAN address, for example:

```sh
./build/nexal-pager donor \
  --keys "$HOME/nexal-pager-test-keys/donor" \
  --listen 192.168.1.20:9443 --pages 64
```

Replace `192.168.1.20` with the donor's real address. Bind only to the intended
private interface; do not open Internet port forwarding or use a Cloudflare Tunnel.
An OS firewall may require narrowly scoped approval for this private test.

On the receiving Mac, after building the helper:

```sh
./build/nexal-pager client \
  --keys "$HOME/nexal-pager-test-keys/client" \
  --addr 192.168.1.20:9443 --cache 4 \
  --native-helper ./build/hvf-pager
```

Remove `--native-helper ...` to run the portable test instead. Stop and restart
the donor between workloads: reconnecting a fresh client to previously written
pages intentionally fails version checks. Keys expire after 24 hours; generate
a new directory rather than overwriting old material.

The native guest's maximum runtime is independently bounded by a watchdog;
the Go workload also has a default 60-second deadline, configurable up to two
minutes. Timeout or donor loss means failure, not recovery; the test contains only
disposable generated data. Do not place real application data in this prototype.

## Implementation map

- `pager/transport.go`: fixed-size protocol, one-session donor, version/digest
  verification, request deadlines, cancellation and no public endpoints.
- `pager/cache.go`: bounded LRU portable cache, dirty writeback and failure retention.
- `pager/keys.go`: short-lived role-separated credentials and TLS policy.
- `pager/native.go`: bounded binary IPC and supervisor for the explicit native helper.
- `native/hvf_pager.c`: one vCPU, 16 KiB mappings, fixed guest code, stopped-vCPU
  eviction, flush/unmap before verification and strict unexpected-exit rejection.
- `native/guest.S`, `native/guest_code.h`: independently checked fill/read/compare
  loops using deterministic xorshift test data, not a cryptographic RNG.
- `tests/test_guest.py`: assembly equivalence and ARM emulation, not native HVF.
- `tests/test_cli.py`: real separate foreground donor/client processes and shutdown.

The C helper does not accept arbitrary guest code. Its mappings intentionally have
no GPU/device aliases, DMA or concurrent vCPUs; those missing mechanisms must not be
assumed correct for a real OS. It handles only expected data translation faults in
this fixed workload and stops on all unsupported faults.

## Security and operational limitations

- Same-user malicious code or replacement of a locally selected binary is outside
  this research tool's trust model; this is not a hardened multi-tenant service.
- Private IP binding is not identity verification. Mutual TLS provides peer
  authentication; physical network routing still needs owner verification.
- An authorized donor could deny service. Integrity metadata detects changed
  acknowledged data, but does not create availability or durable persistence.
- Donor withdrawal aborts this disposable workload; it does not implement live
  memory evacuation or the production connector's owner-first scheduling.
- TLS protects transit. Neither donor payload nor process memory is separately
  encrypted against a compromised host; credentials are ordinary private files,
  not integrated with Keychain in this isolated prototype.
- There is no claim of Thunderbolt emulation, RDMA hardware acceleration, CXL,
  macOS guest boot, native host RAM expansion or extra Metal/VRAM.

## Reproduce verification

```sh
go test -race -count=5 ./...
go vet ./...
mkdir -p build
go build -o build/nexal-pager ./cmd/nexal-pager
python3 tests/test_cli.py
```

Optional developer-only ARM emulation checks:

```sh
python3 -m venv build/test-venv
build/test-venv/bin/pip install -r tests/requirements-test.txt
build/test-venv/bin/python tests/test_guest.py
```

See [VALIDATION.md](VALIDATION.md) for the actual results and untested boundaries.
Apple documents the native primitives used by this experiment in
[Hypervisor](https://developer.apple.com/documentation/hypervisor) and
[`hv_vm_map`](https://developer.apple.com/documentation/hypervisor/hv_vm_map(_:_:_:_:)).
