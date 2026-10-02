# Mesh runtime resilience plan (2026-10-01)

Observed on the gateway: `Failed to initiate handshake ... dial tcp 100.86.28.93:61518` and
`100.86.150.146:57584: i/o timeout`, repeating every couple of seconds. Those two addresses are the
iPhone peers (`iphone-28-93`, `iphone-150-146`). iPhones do not run this runtime and have no
ML-KEM TCP listener, so the gateway's handshake to them can never succeed. Likely noise, not the cause
of Macs losing ML-KEM evidence (see "Diagnose first").

Rule for everything below: Level 5 is never relaxed. Resilience means recovering faster and
explaining failures, not accepting a weaker session. The WireGuard session gate is unchanged by
this work: an expired lease denies application traffic, a peer without the profile never gets the
gate opened, and no path treats a plain WireGuard session as protected.

## Status — implemented in source on 2026-10-01 (not installed anywhere)

Two new patch files, applied last by `prepare.py`: `rosenpass-resilience.patch` and
`netbird-resilience.patch`. New regression tests: `nexal_backoff_test.go` (library) and
`netbird_resilience_test.go` (runtime); the existing evidence/status tests were extended.
Connector source (`connector/internal/mesh`) carries the reason through to local status.

| Plan item | Done | How it was verified |
|---|---|---|
| 1. Peer eligibility | yes | The runtime advertises `nexal-mlkem1024-tcp-v2` as the host part of its signaled Rosenpass address (`nexal-mlkem1024-tcp-v2:<port>`); older runtimes read only the port. `addPeer` registers and dials only peers that advertise it. Others are skipped with one debug line per advertised address, are never registered with the exchange server, never get a control endpoint or gate exemption, and report reason `peer-lacks-profile`. `TestNexalEligibilitySkipsPeersWithoutProfile`, `TestNexalAdvertisedAddressCarriesProfile`. |
| 2. Backoff with jitter | yes | Per-peer consecutive-failure counter in the library: 2 s doubling, +/-20 % jitter, capped at 5 min without a live lease and at 15 s while the lease is live; reset on a completed exchange. A failed attempt is torn down (no handshake left retransmitting for 3 min); first failure logs WARN, later ones DEBUG; `HandshakeFailedHandler` reports each failure and its retry delay. `TestNexalBackoffDelayGrowsAndCaps`, `TestNexalInitiationBackoffOnUnreachablePeer`, `TestNexalTCPHandshake` (recovers in ~5 s instead of ~15 s). |
| 3. Dial budget | yes | `TCPBudget{Dial, Write, Receipt}` per endpoint: direct 3/3/3 s, relayed 6/5/6 s, chosen from the runtime's own peer state (`Relayed`) at registration and on relay-to-direct updates; the accept side holds an unidentified connection at most 3 s. `TestNexalDeliveryBudgetFollowsPath`. |
| 4. Renewal margin | yes | Renewal already starts at 90 s / 100 s of a 180 s lease; retries now continue inside that margin every <= 15 s instead of once per attempt. Key expiry became a peer-level timer armed at completion (lease end) and re-armed every `RejectAfterTime` while the peer stays down, so `HandshakeExpired` keeps its cadence on both ends regardless of how attempts are paced; a renewal that fails long enough still expires exactly at the lease end and the gate denies traffic. `TestNexalLeaseExpiryIndependentOfAttempts`, `TestNexalEndToEndGatedMLKEM` (three automatic renewals, expiry denial, recovery through a fresh exchange, real gated WireGuard devices at MTU 1280). |
| 5. Honest status | yes | New field `quantumReason` (protobuf field 24, JSON `quantumReason`, iOS SDK `QuantumReason`) next to the evidence, empty exactly when evidence is current. Values from the runtime: `exchange-pending`, `peer-unreachable`, `peer-lacks-profile`, `evidence-expired`, `key-install-failed`, `session-pending`. Connector adds `peer-disconnected`, `evidence-stale`, `runtime-not-strict` and exposes `pqReason` on each peer of the local status. `TestNexalReasonFollowsLifecycle`, `TestNexalEvidenceSurvivesStatusRPC`, connector `TestPQReasonSurfacesAndPhoneIsNotCovered`. |
| 6. Phones | partly | A peer reporting `peer-lacks-profile` is shown by the connector as `pq: unsupported` ("not covered") with its lifecycle untouched instead of `degraded`; it still gets no sharing services and does not demote the host's gateway-link claim. Giving the phone app an initiator role was not done (the phone app does not carry this profile). |

Further fixes from the adversarial review (all in the same patches):

- Retransmission callbacks no longer hold the handshake lock across a TCP delivery; before, a
  dial toward an unreachable peer could block `stopRetransmission`, which runs under the server
  state lock, and stall every peer for the dial budget.
- One initiator handshake per peer: a new attempt replaces an unfinished one, and a responder-side
  completion drops our own unanswered InitHello (the peer holds no state before InitConf), which
  shrinks the window for crossed exchanges installing different keys on the two ends.
- Repeated "expired N times, falling back to the rendezvous key" is WARN for the first two
  expiries and DEBUG afterwards; the condition is visible through the status reason instead.

Verified offline on Linux arm64 with Go 1.26.8 (clean `prepare.py` run, zero fuzz, then the
commands from `README.md`): library tests 14/14 (also with `-race`), runtime
`client/internal/rosenpass` 18/18, `client/status` and `shared/management/client` pass.
The WireGuard device suite is unchanged by this work; `TestStagePacketsBoundedPerPeer` fails on
this arm64 host for the pristine (unpatched) fork as well and is unrelated.

Not done or not verifiable here: no live Mac/gateway/iPhone run (the sandbox cannot reach the
hosts and must not start services), no macOS build, the iOS SDK was compiled only as part of
`go vet`/`go build` for Linux, the coordinator report and dashboard do not carry the reason yet
(local status only), and `update-runtime.sh` still builds the old version string
`0.79.0-nexal-mlkem1024-gated.9-mac` — bump it deliberately before rolling out so the two
runtimes can be told apart.

Rollout consequence of item 1: a runtime with this change only initiates toward peers that
advertise the label, and it does not register peers without it, so a mixed pair (one old
candidate, one new) has **no** ML-KEM exchange at all and the new side reports
`peer-lacks-profile`. Upgrade the gateway and the Mac together, as the README already requires.

## Diagnose first (needs the live hosts)

1. On each host that dropped to plain WireGuard: `nexal status` (peer `pq` state, `pqReason`,
   `quantumProfile`, `pqExpiresAt`) and the runtime version. The Mac mini was recorded as
   incompatible/degraded on 2026-09-28 and was never upgraded; it needs the runtime named in
   `macos/scripts/runtime-policy.json`.
2. On the gateway: journal lines for the Mac peers' handshakes (not the phones), looking for the first
   failure after a previously good renewal. With this change the first failure is one WARN line
   (`Failed to initiate handshake; backing off`), later ones are DEBUG.

## Original change list (kept for reference)

1. **Peer eligibility.** Only initiate a handshake with peers whose management record advertises the
   ML-KEM runtime profile. Phones and old runtimes are skipped, with one debug line instead of an error every cycle.
2. **Backoff with jitter** per peer on failed initiation (for example 2 s, 4 s, ... capped at 5 min, +/-20 %),
   reset on success. Today a failure is logged and rescheduled without growth.
3. **Dial budget.** Raise the 2 s TCP dial/write budget for peers reached over relays, keep it short for direct peers.
4. **Renewal margin.** Start renewal earlier, before the lease expires, and keep retrying inside the margin so one
   lost packet cannot expire the key. The gate stays strict: no traffic is allowed on an expired lease (no downgrade).
5. **Honest status.** Keep reporting `degraded` the moment evidence is stale. Add a `reason` (peer unreachable,
   peer lacks profile, evidence expired) so the apps can say why instead of just "WireGuard".
6. **Phones.** iPhones are covered separately (they never carry this profile). Show them as "not covered" rather
   than degraded, or give the phone app an initiator role so the gateway does not dial in.

## Hardening — 2026-10-02 (source only, not installed)

Observed live on the storage gateway: the NetBird daemon panicked in
`golang.org/x/net/internal/socket.sendmmsg` ("index out of range [0] with length 0") when
WireGuard handed the bind an empty batch, and `rosenpass key ... expired 399 times` repeated at
WARN. The WARN level and the stock WireGuard frames in the trace show the gateway was **not**
running this candidate: it was built from nexal-platform's own copy of the bundle, which lacked
both resilience patches. The Mac (`gated-experimental.7-recovery`), gateway (`gated.8-retry`) and
sidecar (`gated.9-linux`) were three different patch levels, so the eligibility rule correctly
refused exchanges between them and every lease expired.

| Change | Where |
|---|---|
| Empty-batch guard in `ICEBind.Send` and `StdNetBind.Send`; the build fails if either signature moves | `prepare.py` |
| Regression test `TestNexalSendEmptyBatch` (wireguard `./conn`) | `wireguard_emptybatch_test.go`, run by `update-runtime.sh` and the gateway build |
| One version for every build: `RUNTIME_VERSION` (`-mac`, `-linux` suffixes) | this directory; `update-runtime.sh`, sidecar `Dockerfile`, nexal-platform `upgrade.py` |
| Connector test `TestMeshRuntimeBuildsAligned`: sidecar/Mac versions, every patch and test used, guard present | `connector/internal/sandbox/runtime_alignment_test.go` |
| Gateway bundle is a synced copy: `sync-mlkem1024.py` (+ `--check`), every bundle file must be in `SHA256SUMS`, test fails on drift | nexal-platform `self-hosted/netbird` |
| Gateway unit: `Restart=always`, `RestartSec=2`, `StartLimitIntervalSec=0` | nexal-platform `upgrade.py` drop-in |
| Sidecar supervises the daemon: restart and rejoin (no setup key needed), give up after 5 restarts in 10 min | `connector/sidecar/entrypoint.sh`; image `0.1.2` |
| Sidecar image build verifies the guard is present | `connector/sidecar/Dockerfile` |

The gate is unchanged: no path admits application traffic without a current ML-KEM lease.

**Rollout rule (unchanged, now enforceable):** every peer must run the same `RUNTIME_VERSION`.
A peer on another patch level is refused (`peer-lacks-profile`) and gets **no** application
traffic through the gate. Upgrade gateway, Macs and sidecar image together:

1. nexal-platform: `python3 self-hosted/netbird/sync-mlkem1024.py`, commit, then on the gateway
   `sudo python3 upgrade.py ...` (builds and runs the suites, then activates with rollback armed).
2. Each Mac: `bash macos/scripts/update-runtime.sh`; then update `runtime-policy.json` for packaging.
3. Sidecar: tag `sidecar-v0.1.2` (the connector now defaults to it).
4. Verify: `self-hosted/netbird/diagnose-pq.sh` — every version line must end in the same
   `gated.N`, and every peer must show `profile=nexal-mlkem1024-tcp-v2`.
