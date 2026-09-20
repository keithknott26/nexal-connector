# Connector panel indicators, graphs and restructuring — acceptance

This change implements HARDENING-PLAN §26 in the menu-bar panel: two separate
always-visible indicators, an active-transport row, four graphs over one bounded
in-memory window, and a status-first restructuring. It also implements the
architecture correction in §29.8 — RDMA capability is per subsystem, behind one
narrow seam, and is reported to this app rather than measured by it.

The menu-bar behaviour itself was already in place (`MenuBarExtra` with
`.menuBarExtraStyle(.window)` and `LSUIElement` in `Resources/Info.plist`) and
was not rebuilt.

## Verification status

- **Nothing in this change was compiled, run or tested.** There is no Swift
  toolchain and no Apple SDK in the workspace where it was written. Every type,
  modifier and initializer used was matched against ones already present in this
  package, but that is a reading, not a build.
- New XCTest cases were added for all presentation logic introduced
  (`TransportCapabilityTests`, `TransportPresentationTests`,
  `RDMAPresentationTests`, `ConnectorHistoryTests`,
  `DomainEscrowPresentationTests`, plus additions to
  `ResourceSharingPresentationTests` and `CLIContractTests`). They have not been
  executed.
- No Go code was modified. No new file is written to disk by the app: the chart
  window is in-memory for the lifetime of the process, so `CONTRACT.md` and the
  connector's telemetry surface are unchanged.
- No change to execution mode, contribution policy, memory admission, consent or
  tunnels.

## What the indicators say, and what they must never say

Two indicators, never merged (§26.3):

- **Resources shareable** — answers "is this Mac contributing". It is the only
  one of the two permitted to signal a real loss of function (§26.6).
- **Thunderbolt RDMA** — a speed fact. Grey means *slower, not unavailable*.

Rules asserted by tests rather than left to review:

- Every grey state pairs colour with a distinct SF Symbol and a text label, and
  carries a reason, so the row reads in grey, in a screenshot and under
  VoiceOver.
- No grey RDMA state contains the words error, failed, failure, problem, broken,
  warning, upgrade, buy or purchase, and none suggests different hardware.
- `unknown` is a distinct state from `unavailable`, and it is the **default**:
  it is what the row shows when no layer is reporting, which is the state today.
- The panel never prints "Thunderbolt 5 detected", and never names a transport
  that is not established.
- No vendor performance figure appears anywhere. Tests assert the absence of
  "99%", "reduction in latency", "300 microseconds" and "3 microseconds". Only
  figures measured on this Mac are shown, and §26.4's throughput chart is the
  only place they would come from.

## The capability seam

`TransportCapabilityProviding` is the single seam. Views, presenters and charts
depend on it; no view reads `status.transport`.

- `TransportCapability` carries the facts: RDMA availability **per subsystem**,
  the established mechanism, the reporting layer's own words for display, a
  source name for attribution, and whether anything is reporting at all.
- `TransportSubsystem` is the per-subsystem key required by §29.8:
  `.computeCollectives` (tensors between inference ranks, owned by the compute
  runtime's collective backend) and `.memoryPager` (paged memory, a separate
  path that cannot reuse that backend). Both are `unknown` today.
- `ConnectorStatusCapabilitySource` is the only implementation. It reads the Go
  connector's `transport` string, resolves a mechanism from it, and reports RDMA
  as `unknown` unless that string names an established RDMA path. It can never
  report `unavailable`: the Go status has no field that positively denies RDMA,
  and reading silence as a denial would be an invention.

## Mac acceptance checklist

- Build and test using `bash macos/scripts/package-app.sh` from the repository
  root, then `swift test --package-path macos`.
- **Cold launch, no connector running.** Three status rows appear: resources
  shareable, Thunderbolt RDMA, active transport. All grey, none styled as an
  error. RDMA reads "Unknown — sharing at TCP/IP speed".
- **RDMA disclosure.** "What this means" lists both subsystems by name, each as
  "unknown, not reported yet", and states that RDMA is per path, not one setting.
- **On a Thunderbolt 4 Mac** (both founder Macs): the RDMA row stays grey
  indefinitely and must read as ordinary and informative. Confirm it never
  presents as a fault and that no text suggests buying different hardware.
- **Transport row.** With the connector running, the reason line contains the
  connector's own transport string verbatim, including its own warnings.
- **Charts.** All four read "no data yet" with a stated reason before the first
  successful poll, and none draws a flat line at zero. After three polls (30s),
  memory, job activity and owner activity begin drawing. Throughput stays empty
  permanently and says why.
- **One poll.** Watch the connector log: exactly one status call every ten
  seconds with the panel open, not one per chart.
- **Window close and reopen.** The in-memory window survives while the app runs
  and is empty again after a relaunch. No new file appears in the connector's
  state directory.
- **Honesty strings.** Confirm each of these is present, verbatim: "Production
  dispatch not verified"; "Telemetry unknown — Go admission fails closed";
  "Synthetic development telemetry"; "Owner active; private work explicitly
  permitted"; "Owner active; owner priority applies"; "Owner idle"; "Waiting:
  …"; "Ready for an eligible job, or currently executing one."; "You explicitly
  permitted private work while active."; "Permission ends: …"; "Checked … ago";
  "An active permission window is not extended by repeated clicks."; the "No
  idle wait: …" paragraph; "Opt-in permits the Go connector to apply its
  resource and idle policies. It does not promise that a workload is available
  or enabled."; "Cloud / marketplace contribution is gated"; "Private membership
  is not public consent. This build has no public execution, earnings, or
  automatic paid fallback."; the setup block including "Existing configuration is
  preserved. …", "The dots indicate completed enrollment. The one-use code is not
  retained." and the approved-CPU-template paragraph; "Native preview • macOS
  14+"; and both quit sentences.
- Two of those moved into disclosures to stop them crowding the layout and are
  not deleted: the "No idle wait: …" paragraph is under "What \"accept jobs now\"
  permits", and per §25.4 a new always-present "Who can read your files"
  disclosure states that nobody but the owner can read Nexal @ Home while
  company administrators can recover and therefore read Nexal @ Work.
- VoiceOver reads each indicator as one sentence — heading, label, then reason.

## Known gaps, all owned by the Go status schema

These are reported rather than worked around; no number is invented to fill them.

- **No per-transport byte counters.** The throughput chart is therefore
  permanently empty and says so. It is the one place a measured figure would ever
  be shown.
- **No RDMA, Thunderbolt or link-capability field.** Hence `unknown` for both
  subsystems. `connector/internal/pool/placement.go` has `Thunderbolt` and
  `RDMAEnabled` on its MLX peer type, but those are not surfaced in status.
- **No job counters.** Status reports one `activeAttempt` and a `lastOutcome`,
  so accepted and running cannot be separated. The job chart counts attempt
  fingerprints observed at the poll and states that limit next to itself.

## References

- Apple, *TN3205: Low-latency communication with RDMA over Thunderbolt* —
  https://developer.apple.com/documentation/technotes/tn3205-low-latency-communication-with-rdma-over-thunderbolt
- `docs/HARDENING-PLAN.md` §26 (connector UI), §26.6 (speed, not capability),
  §25.4 (escrow legibility), §29.8 (RDMA lives in MLX; per-subsystem capability)
  in `nexal-platform`.
- `docs/remote-memory-transport-research.pplx.md` in `nexal-platform` for the
  fixed transport product names.
