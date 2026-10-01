# Mesh runtime resilience plan (2026-10-01)

Observed on the gateway: `Failed to initiate handshake ... dial tcp 100.86.28.93:61518` and
`100.86.150.146:57584: i/o timeout`, repeating every couple of seconds. Those two addresses are the
iPhone peers (`iphone-28-93`, `iphone-150-146`). iPhones do not run this runtime and have no
ML-KEM TCP listener, so the gateway's handshake to them can never succeed. Likely noise, not the cause
of Macs losing ML-KEM evidence (see "Diagnose first").

## Diagnose first (needs the live hosts)

1. On each host that dropped to plain WireGuard: `nexal status` (peer `pq` state, `quantumProfile`,
   `pqExpiresAt`) and the runtime version. The Mac mini was recorded as incompatible/degraded on
   2026-09-28 and was never upgraded; it needs the runtime named in `macos/scripts/runtime-policy.json`.
2. On the gateway: journal lines for the Mac peers' handshakes (not the phones), looking for the first
   failure after a previously good renewal.

## Changes to build (each needs `prepare.py` plus the full test suite on a Mac)

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

Rule for all of the above: Level 5 is never relaxed. Resilience means recovering faster and explaining
failures, not accepting a weaker session.
