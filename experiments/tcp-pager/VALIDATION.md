# Bounded TCP pager validation

Checkpoint: September 19, 2026. This evidence covers the isolated research
module, not production OS memory expansion or successful native Mac execution.

## Executed in the Linux development environment

- Go 1.26.0: `go test -race -count=5 ./...` passed.
- One uncached JSON test run: 21 top-level Go tests, 29 named passes including
  subtests. They cover cache limits, TCP round trips, page versions, corruption,
  lost acknowledgements, cancellation, donor loss, concurrency, credential
  permissions, authentication, refusal of classical-only key exchange,
  native IPC validation and one-session admission.
- `go vet ./...` passed.
- Pager package statement coverage: 78.8%. Native Mac execution/supervision is
  not exercised on Linux; this is not whole-product coverage. The CLI's separate
  subprocess tests are not included in Go statement coverage.
- Four Python CLI test methods passed, including separate donor/client processes,
  no-overwrite key initialization, actual signal shutdown and Linux rejection of
  native mode.
- Three Python ARM validation test methods passed: independent assembly matches
  the embedded instructions; every 64-bit word is written/read correctly for
  page seeds 0, 31 and 255; corruption at the start, middle and end is detected.
  These use Keystone 0.9.2 and Unicorn 2.1.4, not Apple's Hypervisor framework.
- Go executable built on Linux and cross-built for Darwin ARM64. This cross-build
  does not compile or link the C Hypervisor helper.
- Shell syntax checks passed for both Mac build/acceptance scripts.
- Existing connector module: `go test -race ./...` and `go vet ./...` passed.
  The new module is not imported by that connector.

## Observed portable results

| Check | Default run | Maximum bounded run |
|---|---:|---:|
| Logical dataset | 1,048,576 bytes | 4,194,304 bytes |
| Resident cache payload cap | 65,536 bytes | 262,144 bytes |
| Verified data | 1,048,576 bytes | 4,194,304 bytes |
| Cache misses | 128 | 512 |
| Evictions | 120 | 480 |
| Peak resident pages | 4 | 16 |
| TCP GET operations | 128 | 512 |
| TCP PUT operations | 64 | 256 |
| Page bytes received | 2,097,152 | 8,388,608 |
| Page bytes sent | 1,048,576 | 4,194,304 |
| Verification | Passed | Passed |
| Native HVF execution | Not executed | Not executed |

Both runs used a loopback donor in the Go process. They establish transport/cache
correctness for the tested workload, not extra available host RAM, remote physical
LAN performance, latency targets or protection against every failure.

## Native implementation awaiting acceptance

The C helper implements a fixed guest workload, public Hypervisor VM creation,
one vCPU, bounded page mappings, translation-fault handling, stopped-vCPU eviction,
remote writeback, refetch and guest-side data comparison. Its first real Mac build
may expose SDK, entitlement or runtime differences; those must be fixed from actual
compiler/error output, not bypassed by disabling security.

Not performed:

- Native C compilation/link/signing on Apple's SDK.
- Real `hv_vcpu_run`, page-fault exits, mapping replacement or cleanup.
- Actual private-LAN two-computer paging.
- macOS guest boot, arbitrary native application support, Metal/VRAM expansion,
  DMA integration, concurrent vCPUs or a 100 GB guest.
- Signed/notarized distribution or production connector integration.

Next owner acceptance command, from this module:

```sh
bash scripts/accept-macos.sh
```

Share the output, especially the native JSON or the first compiler/Hypervisor error.
No security downgrade is part of this acceptance plan.
