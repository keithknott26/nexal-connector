# Bounded TCP pager validation

Checkpoint: September 19, 2026. This evidence covers the isolated research
module, including owner-reported native loopback acceptance. It does not establish
production OS memory expansion or paging between separate computers.

## Owner-reported native Mac acceptance: passed

The owner supplied successful output from `bash scripts/accept-macos.sh`.
The reported toolchain was Go 1.26.8 on darwin/arm64. The output shows passing
Go tests, native helper build completion, valid local code signature, and
successful portable and native loopback workloads. This was run on the owner's
Mac, not independently executed in the Linux development environment. In a
follow-up, the owner identified the receiving/test Mac as the M2 Mac mini and
confirmed both Macs are on the same local network. The exact model identifier,
macOS version and checkout revision are not established by this output.

Both workloads reported:

- Logical dataset and verified bytes: 1,048,576 (1 MiB).
- Cache payload limit: 65,536 bytes (64 KiB); peak resident pages: 4.
- Faults/cache misses: 128; evictions: 120.
- TCP GETs: 128; PUTs: 64.
- Page bytes received: 2,097,152; sent: 1,048,576.
- Verification: true.

The native result reports `nativeHVFExecuted: true`; the portable result correctly
reports false. Both report `macOSGuestBooted`, `hostRAMExpanded` and
`gpuMemoryExpanded` as false. Counts match the expected bounded workload.
The native path exercises CPU-fault exits, mapping replacement, writeback,
refetch and fixed-guest data verification. Passing process completion is not
an exhaustive cleanup, resource-leak or fault-injection acceptance result.

This passes the small native CPU-pager loopback gate only. The donor ran on the
same computer; the 16:1 logical-dataset/cache-payload ratio is not a 16x increase
in system RAM, and additional runtime allocations are outside the cache cap.
See [the transcribed acceptance record](validation/native-owner-acceptance.json)
for provenance and the reported JSON results.

## Executed in the Linux development environment

### Guided lab and OS-observation increment

The new increment was tested separately after the owner's original native pass:

- `go test -race -count=3 ./...`: passed.
- One uncached JSON run: 36 top-level tests, 59 named passes including subtests.
- `go vet ./...`: passed.
- Six new Python CLI process tests passed, including repeated fresh donor
  sessions, fingerprint rejection before connection, preservation of existing
  output, and exit 3 rather than false OS-visible RAM acceptance.
- Four original Python CLI tests and three ARM assembly/emulation tests passed.
- Portable C allocator tests passed with AddressSanitizer and UndefinedBehaviorSanitizer.
- Shell syntax passed for all Mac scripts.
- Go lab CLI cross-built for Darwin ARM64.
- Statement coverage: lab package 71.7%, pager package 79.4%; not native Mac,
  Core Foundation, the CLI package or whole-product coverage.

The revised C helper's optional observation hold, native host counter sampler,
Core Foundation API calls, Finder reveal and new full Mac script were NOT
executed here. The original M2 result is not evidence for these new additions.
No separate-Mac LAN test, full guest OS or host/GPU RAM expansion was performed.
See [the runbook](MAC-TEST-RUNBOOK.md) and [RAM acceptance contract](OS-VISIBLE-RAM-ACCEPTANCE.md).

### Original prototype evidence

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

## Remaining acceptance

The C helper implements a fixed guest workload, public Hypervisor VM creation,
one vCPU, bounded page mappings, translation-fault handling, stopped-vCPU eviction,
remote writeback, refetch and guest-side data comparison. Its first owner-reported
Mac acceptance passed as recorded above. Other machines and SDK versions still
require their own acceptance; errors must not be bypassed by disabling security.

Not performed:

- Actual private-LAN two-computer paging.
- Native donor-loss, stalled-network, cancellation and resource-leak acceptance.
- Native larger bounded runs and measured private-LAN latency/throughput.
- macOS guest boot, arbitrary native application support, Metal/VRAM expansion,
  DMA integration, concurrent vCPUs or a 100 GB guest.
- Signed/notarized distribution or production connector integration.

Reproduce the passed loopback gate, from this module:

```sh
bash scripts/accept-macos.sh
```

Next, follow the README's two-computer instructions with the same 64-page dataset
and four-page cache. Record donor/receiver models, OS versions, source revision,
actual link type and result. No security downgrade is part of this acceptance plan.
