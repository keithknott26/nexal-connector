# Nexal Mac memory acceptance runbook

Status: scripted research acceptance, not production memory expansion.
For encrypted delivery through the coordinator instead of AirDrop, see
[platform delivery](PLATFORM-DELIVERY.md). It requires the updated coordinator,
migration 0006 and both Macs enrolled in the same reachable service.
The original native CPU pager passed on the owner's M2 Mac mini. The owner
subsequently passed the observation-window and Core Foundation diagnostics at
revision `78bae3a` on an 8 GiB Mac. The guided separate-Mac LAN workflow remains
untested. Both Macs are on the same owner-reported LAN.

## Test everything locally on the M4 or M2

### Guided prerequisites and restart-safe LAN setup

From this module, run `bash scripts/setup-lan-macos.sh --donor` on the M4.
It checks native Apple-silicon architecture, Apple build tools/SDK and stable
Go 1.26+ before starting. If Go is missing or outdated, it asks for approval to
install `go@1.26` through existing Homebrew. Dependencies may also change.
It does not install Homebrew itself, change global Go links or shell profiles,
accept Apple licenses, weaken security, reboot or register a login task.

On the M2, after transferring the NEW donor client folder, use:

```sh
bash scripts/setup-lan-macos.sh --receiver "$HOME/Downloads/client"
```

Use `--check` for read-only prerequisites (nonzero if missing), or `--no-start`
to prepare prerequisites without building or listening. Each invocation prints
its absolute rerun command. If Apple tooling requests a restart, complete it
and rerun that command; setup rechecks installed prerequisites, not saved flags.
The Go version mismatch itself is not a reboot instruction. A restarted donor
creates fresh credentials; transfer its new client folder rather than reusing
the previous session's bundle. Nothing resumes automatically during boot.

`NEXAL_PAGER_GO` can explicitly select an absolute path to a stable Go 1.26+
darwin/arm64 executable. Invalid overrides stop setup rather than silently
installing or selecting a different compiler. Direct build scripts remain
non-installing and now reject old Go before attempting compilation.

The wrapper has Linux mock-toolchain regression coverage, not actual native
Homebrew installation/reboot acceptance. Run the checks on your Macs to establish
that separate acceptance.

From `experiments/tcp-pager` in the updated connector checkout:

```sh
bash scripts/accept-memory-macos.sh
```

This builds the isolated tools and runs:

- Portable TCP paging baseline.
- Native Hypervisor CPU-fault paging baseline.
- A real `CFAllocatorCreate` diagnostic using bounded local allocations.
- A four-case paging suite with actual macOS `hw.memsize` readings before,
  during and after the native pager sessions.

It does not need the other Mac, download packages, modify system settings,
change the default allocator, start a public listener, disable security or touch
Nexal enrollment. Existing Go 1.26+ and Apple command-line tools are required.

The native observed cases hold the final cache mappings for one second while
the vCPU is stopped, allowing the host counter sampler to run. This is not a
guest operating system. At most 16 recent samples per native case are retained.
The one-second hold and sampling overhead are included in workload durations;
do not treat those durations as pure network or paging performance.

### Exit codes are deliberately different

| Exit | Meaning |
|---|---|
| 0 | A component diagnostic or ordinary paging suite passed. Not proof of OS RAM expansion. |
| 1 | Build, runtime, identity, connection, data-verification or other test error. Stop and inspect the first error. |
| 3 | Paging passed, but the explicitly requested OS-visible RAM requirement is not implemented and therefore has not passed. |

The all-in-one script is expected to finish with **3**, not 0, if all implemented
components work. This is a release requirement check, not a claim that an
OS-memory backend exists and merely needs testing.

Actual counter readings are recorded, not synthesized. The report may show
`unchanged_in_available_samples`, `insufficient_available_measurements`, or
`counter_changed_requires_investigation_not_proof_of_expansion`. A changed number
alone cannot satisfy the requirement: ordinary OS allocations must actually use
the network-backed pages correctly. None of the current tools implements that
host or full-guest OS integration.

## Run only the requested OS-visible RAM check

From the same directory, with no donor required:

```sh
bash scripts/test-os-visible-ram-macos.sh
```

This builds and runs the observed native loopback suite. It measures host
physical-memory counters, not GPU residency, a full guest OS allocator, or
additional physical hardware. Exit 3 preserves the unimplemented requirement.

To isolate the API linked by the owner:

```sh
bash scripts/test-cfallocator-macos.sh
```

That diagnostic tests callback use, reallocation/data preservation, a normal
Core Foundation `CFData` object and release accounting. Its custom allocator
is local-only, single-threaded and limited to 256 KiB of live payload.
It deliberately does not replace the thread/process default allocator or
pretend to be a remote-memory implementation.

## Two-Mac test: M4 donor, M2 receiver

### On the M4

From the updated `experiments/tcp-pager` directory:

```sh
bash scripts/lan-donor-macos.sh
```

The script builds the tools, selects the sole private interface or asks you
to choose one, creates fresh 24-hour test credentials outside the repository,
and starts the disposable donor. Verify the chosen address belongs to the
intended local interface, not a VPN. You can explicitly select an address:

```sh
bash scripts/lan-donor-macos.sh --listen 192.168.1.20:9443
```

Replace that sample address with the actual M4 LAN address.
The process prints a client-folder path and a public CA fingerprint, and
attempts to reveal the folder in Finder. Leave that terminal open.

- Transfer **only the `client` folder** to your M2 using a trusted authenticated
  transfer, such as AirDrop to your own confirmed Mac.
- Do not transfer the sibling donor folder or post private keys in chat/GitHub.
- Compare the public CA fingerprint using the donor's terminal.
- No Internet forwarding, Cloudflare Tunnel or firewall disabling is required.
  If macOS asks about network access, approve only the reviewed test binary on
  the intended private network.

The donor permits at most 16 authenticated sessions and stops after 30 minutes
or Ctrl+C. Each connection receives a **new disposable zero-filled store**.
This is lab reset behavior, not reconnect/recovery of live memory.
It reserves 1 MiB of accessible donor payload per live session, plus runtime,
TLS, temporary and garbage-collection overhead; total RSS is not capped at 1 MiB.

### On the M2

From the updated `experiments/tcp-pager` directory, using the transferred folder:

```sh
bash scripts/lan-receiver-macos.sh "$HOME/Downloads/client"
```

Adjust that path if needed. The script asks you to paste the **public fingerprint**
printed on the M4. It imports only the four expected credential/manifest files
into fresh private storage; it never executes anything from the transfer folder.
The import repairs read-permission differences by making its own 0700/0600 copy,
but does not secure or delete the original transfer copy. Keep that copy private
and remove it after the experiment when no longer needed.

The suite runs:

| Case | Logical dataset | Receiver cache payload |
|---|---:|---:|
| Portable baseline | 1 MiB | 64 KiB |
| Native CPU-fault paging | 1 MiB | 64 KiB |
| Native eviction stress | 1 MiB | 16 KiB |
| Native repeat with fresh donor store | 1 MiB | 64 KiB |

Expected per-case counts: 128 GETs, 64 PUTs, 128 faults/cache misses; 120
evictions with four cache pages, 126 with one page. Native cases must report
actual native execution; portable cases must not.

To additionally enforce the still-unimplemented OS-visible RAM requirement:

```sh
bash scripts/test-os-visible-ram-macos.sh "$HOME/Downloads/client"
```

This performs the real LAN paging tests and saves actual host measurements,
then exits 3 if paging passed but the missing OS integration remains.

## Results, retry and safety

- Native suite results are saved as `report.json` under the private evidence
  directory printed by the tool, beneath `~/Library/Application Support/Nexal Pager Lab/`.
  Adjacent imported credentials are secret: share the report, not the whole directory.
- Standalone baseline/CF diagnostic JSON is under ignored `build/`.
- If a donor run expires, start the donor script again and transfer its new
  client folder. It never overwrites old state; old bundles must not be reused.
- Each ordinary suite uses four sessions. A donor can support four full suites
  within its session/time limit; failed authenticated connections also consume
  sessions. This is not unlimited service.
- Reports distinguish a nonlocal IP from proven separate physical machines.
  Confirm the physical donor/receiver and route yourself; private addressing
  is not hardware attestation or proof of a fast direct link.
- Stop the donor with Ctrl+C. Do not kill unrelated listeners to free a port.
- No live application data, public marketplace resources, billing, cloud
  fallback, Keychain changes, firmware changes or security reductions are used.
- Hybrid TLS key agreement remains mandatory; Ed25519 certificate signatures
  are not fully post-quantum authentication.

## What would pass the actual RAM requirement?

The full acceptance contract and assessment of the owner's Apple API link are
in [OS-VISIBLE-RAM-ACCEPTANCE.md](OS-VISIBLE-RAM-ACCEPTANCE.md). Passing CPU paging
or a custom allocator test does not close the host OS, full guest OS or GPU gates.
# Ethernet and Wi-Fi address labels

From the connector repository root, list addresses without starting a donor:

```sh
git pull --ff-only
bash experiments/tcp-pager/scripts/setup-lan-macos.sh --network-info
```

This uses the existing consent-based Go prerequisites, builds a separate
network-information executable, and lists active private addresses. It does not
start a listener, change network settings, or interrupt an existing donor.
The usual `--donor` command now shows the same connection labels during address
selection and prints the connection beside the final donor endpoint.

Illustrative output only (actual interfaces and IPs depend on the Mac):

```text
IP address | Connection type | Interface | macOS hardware port
1) 192.168.1.20 | Ethernet (wired) | en0 | Ethernet
2) 192.168.1.30 | Wi-Fi | en1 | Wi-Fi
```

To choose and start a donor, run:

```sh
bash experiments/tcp-pager/scripts/setup-lan-macos.sh --donor
```

The donor always asks `Choose a connection (1-N), or q to cancel:`, including
when there is only one option. Enter `1` for Ethernet in the illustrative list
above, or the actual number beside Ethernet on your Mac. Invalid entries
prompt again; `q` or end-of-input stops without starting a donor. The
`--network-info` mode remains a numbered diagnostic list, not an interactive
listener launcher. Numbering reflects the current address list and is not
persisted; one interface can have separate IPv4 and IPv6 options.
An explicit `nexal-pager-lab donor --listen IP:PORT` still bypasses the menu.
An already running donor is not updated in place. Stop it deliberately before
starting another donor; a new run produces a new client bundle.

On macOS, hardware-port labels come from the bounded, read-only
`/usr/sbin/networksetup -listallhardwareports` command. Interface numbering is
not used to guess Ethernet versus Wi-Fi. If metadata is unavailable or
unrecognized, the output says **Unknown connection type**. VPN/tunnel, virtual
bridge and Apple peer-to-peer interfaces are labeled separately; a configured
Thunderbolt bridge is not evidence of Thunderbolt 5, RDMA or measured link speed.
Down interfaces, loopback, public addresses and link-local addresses are excluded
from the private candidate list. Private IPv4 and IPv6 ULA addresses are included.

The TCP pager can use a reachable Ethernet or Wi-Fi private address. Select the
donor address reachable from the receiver; labels do not prove firewall
permission, routing, absence of wireless client isolation, or physical LAN
membership. The receiver may use a different connection type from the donor.
The donor binds one selected IP, not all interfaces, and does not implement
Wi-Fi/Ethernet bonding or automatic failover. If the address changes, stop and
restart the donor and deliver the new client bundle. Selecting an IP is not
an OS routing/interface pin, especially if the same IP is assigned twice.

The remote donor's physical connection type cannot be inferred from its IP
alone; the receiver directs the owner to the donor's terminal rather than
inventing that label. Cloud coordinator enrollment still uses ordinary OS
routing and is separate from the selected private pager endpoint.

Machine-readable interface metadata is available through
`build/nexal-pager-lab addresses --details`; the existing `addresses` command
without flags preserves its original bare-IP JSON shape.

Validation: six new Go network tests; full module race tests and vet;
seven real portable CLI tests; 12 setup and eight enrollment mock tests;
Darwin ARM64 cross-build. Actual M4/M2 interface detection and physical
Wi-Fi/Ethernet connectivity remain owner acceptance tests.
