# OS-visible RAM: acceptance contract and CFAllocator assessment

## The requested outcome

The owner wants network-backed capacity that existing software can use as RAM
or VRAM, not just remote job execution, a cosmetic capacity counter or an
explicit page-store API. Keep host macOS, guest OS, cooperating application,
and GPU requirements separate.

## What the supplied Apple API actually provides

`CFAllocatorCreate` creates an allocator object containing callbacks for allocation,
reallocation and deallocation, and that object can be supplied to Core Foundation
creation functions. This is a legitimate application integration point.
([Apple CFAllocatorCreate](https://developer.apple.com/documentation/corefoundation/cfallocatorcreate(_:_:)))

The allocation callback returns a pointer to the beginning of the allocated block;
it does not return a page-store key or remote network reference.
([Apple CFAllocatorAllocateCallBack](https://developer.apple.com/documentation/corefoundation/cfallocatorallocatecallback))

`CFAllocatorSetDefault` sets the default for the current thread for calls using
the default allocator argument. It is not documented as a system-wide replacement
for memory allocation in unrelated applications.
([Apple CFAllocatorSetDefault](https://developer.apple.com/documentation/corefoundation/cfallocatorsetdefault(_:)))

Engineering conclusion: a Nexal allocator could be an opt-in adapter for cooperating
Core Foundation code, but remote backing would still require a working mechanism
that makes the returned address usable by CPU loads/stores. The allocator factory
itself is not that pager and does not establish host RAM or GPU expansion.
This conclusion follows from the API's callback and object-creation scope, not
from a failed native experiment.
([Apple allocator scope](https://developer.apple.com/documentation/corefoundation/cfallocatorcreate(_:_:)),
[callback pointer contract](https://developer.apple.com/documentation/corefoundation/cfallocatorallocatecallback))

Our current Hypervisor helper pages addresses inside a small guest execution
environment. It does not supply transparent remotely backed pointers to host
Core Foundation objects. A host-process fault handler and safe concurrency,
writeback, exception, lifetime and failure semantics would be separate work.

## Added experiments

- `native/cfallocator_probe.c` uses the real Apple API with a deliberately
  local-only 256 KiB bounded allocator. It creates and verifies a normal CFData
  object, exercises allocate/reallocate/free callbacks and records real host
  `hw.memsize` before/during/after. It neither sets a new default allocator nor
  claims remote backing.
- `test-os-visible-ram-macos.sh` runs actual CPU paging and reads host counters
  before/during/after native sessions. It preserves an explicit unimplemented
  requirement even if lower-level paging succeeds.
- Native observed sessions hold their final bounded cache mappings for one
  second, with the vCPU stopped, so host observation is not limited to a point
  after mappings have already been destroyed.
- Host memory counter samples are real observations. The expansion verdicts
  remain false because the current implementation has no OS allocator or
  GPU integration to validate. A changed counter triggers investigation, not
  automatic acceptance.

These are tests of implemented components and their boundary, not a hidden
complete RAM-expansion feature. Native results for these new components must
come from the owner; Linux unit tests are not substitutes.

## Separate pass criteria

### Cooperating host application

Required evidence before claiming an application-level remote heap:

- Ordinary pointer reads/writes in the cooperating application touch a logical
  working set larger than the measured local payload cache.
- Page-fault/read/writeback evidence proves data is actually fetched from a
  distinct donor, not merely allocated as host RAM or written to local swap.
- Allocation, reallocation and lifetime contracts remain valid, with bounded
  resource use and tested exception/thread behavior.
- Donor loss produces a defined failure, not corrupted data, zero-filled
  substitute pages or unbounded hangs.

The current CFAllocator probe proves none of the remote-heap criteria.

### Full guest OS

Required evidence before claiming RAM usable by an OS inside a VM:

- Boot an identified unmodified guest OS with the custom backing integration.
- The guest's own allocator recognizes the additional addressable capacity.
- A normal guest application allocates, touches, evicts and verifies data in
  that range using its normal allocator, without application-specific fetches.
- Measure local residency and actual remote page traffic, account separately
  for local swap/compression and donor memory, and test loss/concurrency.
- Verify the relevant CPU mappings, MMU transitions, timers, exceptions and
  device paths. The existing single-vCPU bare-metal workload is not this test.

An increased guest boot memory setting or displayed total is insufficient.
No full guest OS memory backend is implemented in this prototype.

### Native host macOS

Required evidence before claiming extra host system RAM:

- Identify and implement an actual supported or reviewed OS integration that
  exposes network-backed capacity to the host's allocator.
- Demonstrate ordinary unrelated host applications using that capacity with
  correct data and measured remote backing.
- Explain memory-pressure reporting, local reserve, sleep/wake, donor withdrawal,
  security, failure and recovery behavior.

Reading or changing `hw.memsize` is not such an integration. The current
host-counter test is observational; it cannot by itself prove usable expansion.

### GPU/VRAM

Require actual Metal allocations and completed GPU computation with correct
results, measured residency and remote transfer behavior. CPU pager success,
Core Foundation callback success and a unified-memory capability flag are not
GPU acceptance.

## Next implementation decision

The CFAllocator hook is worth retaining for a separately scoped, cooperating-app
remote heap experiment. It does not eliminate the custom host-process pager work.
The alternative full-guest route requires a real VMM/OS integration, not simply
extending the current bare-metal RAM-size argument. Neither should be advertised
as host-wide transparent RAM until its own acceptance criteria pass.
