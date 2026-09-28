# Experimental ML-KEM-1024 mesh runtime

**The current source candidate is `nexal-mlkem1024-tcp-v2`. It has not been
installed on the owner's peers or shipped in the normal application packages.**
The previously installed local runtime is the older evidence-only experiment;
see the historical installation records below. Do not advertise either as a
reviewed or certified Category 5 product.

This directory pins and patches three dependencies: NetBird v0.79.0,
`cunicu.li/go-rosenpass` v0.5.42, and NetBird's WireGuard-Go fork at
`v0.0.0-20260914123147-8bf8fa968f1a`. Complete pristine-tree hashes are recorded in
`upstream.json`. Original upstream notices remain in the patched sources.

## What the candidate implements

- Independent standardized ML-KEM-1024 keys for the static and ephemeral KEM
  roles, using Go's `crypto/mlkem`; canonical public-key and private-key checks.
- An experimental transcript domain and packet type range. This is a new
  protocol construction, not an upstream Rosenpass security assurance.
- Authenticated envelopes carried in bounded TCP frames: maximum 4,096 bytes,
  registered peer endpoints only, read/write deadlines, bounded receive queues
  and concurrent readers. TCP segmentation carries the 3,240/3,312-byte KEM
  messages without relying on IP fragmentation. Envelope validation remains
  mandatory; TCP is not the authentication mechanism.
- A WireGuard **session-generation gate** at encryption, queued send and
  authenticated receive boundaries. It covers ordinary TUN packets and direct
  forwarding/exit-node packet injection. Application data requires a session
  derived from the currently installed ML-KEM output key and an unexpired lease.
  Old WireGuard traffic keys cannot satisfy a new key generation.
- Before protection and during recovery, only empty keepalives and the exact
  peer-to-local/peer TCP control-port traffic are exempt. Bootstrap fragments,
  extension headers, other addresses/ports, and other protocols are denied.
- Reconnection, key-write failure, expiry, fallback IPC key writes, interface
  replacement and shutdown revoke application authorization. Lease deadlines
  are normalized to monotonic local time. Unsupported kernel WireGuard and
  permissive/disabled PQ configuration fail startup rather than bypass the gate.
- Ordered handshake lifecycle callbacks, bounded failed-first-send retries and
  expiry, automatic retries after a failed renewal expires, stale-generation expiry suppression, and stopped shutdown timers.
- Status evidence is emitted only after a current WireGuard session exists for
  the ML-KEM key generation. Both protobuf conversions and JSON carry
  `quantumProfile`, `quantumKeyInstalledAt`, and `quantumKeyExpiresAt`.
- Connector source accepts only `nexal-mlkem1024-tcp-v2`, rejecting the previous
  installation-only evidence profile. Its existing two-minute freshness ceiling
  remains conservative. macOS UI source displays the Category 5 **parameter**
  label only for fresh matching evidence and identifies it as experimental.

No algorithm name is inferred from an enabled flag or ordinary tunnel handshake.
No new polling over Cloudflare was added; enforcement is local to each packet.

## Reproduce the source and tests

Use Go 1.27.1 (the tested toolchain), Python 3 and `patch`. Obtain the exact
pristine sources listed in `upstream.json`, including the pinned NetBird archive.
The module source paths can be obtained with `go mod download -json` for the exact
module versions. Do not edit module-cache sources.

```sh
python3 experiments/mlkem1024-mesh/prepare.py \
  --netbird-source /absolute/path/to/netbird-v0.79.0 \
  --rosenpass-source /absolute/path/to/go-rosenpass-v0.5.42 \
  --wireguard-source /absolute/path/to/pinned-wireguard-go \
  --output /absolute/path/to/new-candidate
```

The output must not exist. Preparation checks all three tree hashes, applies
zero-fuzz patches, copies tests and wires local module replacements. It never
installs, starts or stops a service. These hashes establish reproducibility, not
an independent audit.

Run these in the respective prepared directories:

```sh
# rosenpass
 go test -race -run 'TestNexal|TestMessages' -count=3 -timeout 120s .
# wireguard
 go test -race ./device -count=1
# netbird
 go test -race -mod=mod ./client/internal/rosenpass ./client/internal/peer ./client/status \
   -run 'TestNexal|TestHandshake|Test.*Status' -timeout 120s
 CGO_ENABLED=0 go build -mod=mod -trimpath \
   -ldflags='-s -w -X github.com/netbirdio/netbird/version.version=0.79.0-nexal-mlkem1024-gated-experimental' \
   -o ../nexal-network-gated ./client
```

Legacy Rust interoperability tests intentionally cannot pass for this new profile.
Legacy manager fixture keys are also incompatible. The full WireGuard device
suite is run, but this is not a claim that every NetBird upstream test passed.

The combined `TestNexalEndToEndGatedMLKEM` uses two real WireGuard devices and TCP
network stacks at MTU 1280. Actual ML-KEM envelopes pass through the gate. The test
asserts application denial before exchange, successful application echo after
exchange, matching active-session evidence, denial after expiry, and recovery
through a fresh exchange. Separate tests cover multiple renewals, TCP frame
limits/reassembly, invalid envelopes, endpoint revocation, old key generations,
fallback writes, status serialization, and stale/missing UI evidence.

## Required rollout and remaining security work

1. Upgrade both endpoints together; this candidate does not negotiate down to
   old Rosenpass or the UDP experiment. Obtain out-of-band access and preserve
   rollback binaries. The owner's Mini M4 and gateway access details are still
   needed. Enabling the local gate first would block those unupgraded peers.
2. The Linux gateway must use **userspace WireGuard**, for example the existing
   `NB_WG_KERNEL_DISABLED` setting. Kernel backends deliberately cannot start
   this strict candidate. Validate routing, existing connections, restart,
   remote access and rollback on the actual gateway before activation.
3. Test real macOS peers, NAT/relay paths, packet loss, TCP interruption and
   reconnect under load. Rootless paired-stack tests are strong integration
   evidence, not a substitute for this device/network matrix.
4. The candidate iOS NexalMesh SDK builds successfully for arm64 devices and
   arm64/x86_64 simulators using `scripts/build-quantum-mesh-sdk.py`. The normal
   release script still downloads stock NetBird. Validate control sockets,
   Network Extension lifecycle, mobile sleep/reconnect and exit routing on a
   physical device. The iOS app has not been upgraded by this candidate.
5. Connector reports, coordinator persistence/read validation, dashboard, and iOS
   now carry the exact profile, parameter category, original verification time,
   and expiry. UI labels require fresh matching evidence and identify the runtime
   as experimental/self-reported. Deploy this coordinator contract before the new
   connector: the previous validator rejects its additional profile/expiry fields.
   No new polling loop is added. Dashboard telemetry adds one tenant-scoped batched
   D1 read per existing telemetry request (bounded to 200 device records).
   These source changes have not been deployed. Existing migration 0055 supplies
   the JSON evidence column; verify it is applied in the target environment.
6. Review key authenticity from enrollment through management/signaling and
   rotation. Existing classical certificate/identity signatures do not become
   quantum-safe because a KEM is upgraded. Specify a reviewed authenticated key
   binding and migration before a full active-quantum-adversary claim.
7. Review the modified construction, transcript/hash security, key lifecycle,
   replay/DoS behavior and gate concurrency independently. No formal security
   proof or external audit was performed. Standardized KEM parameters do not
   transfer an upstream protocol proof to a modified protocol.
8. The broader product is not uniformly Category 5: Drive and bundle-transfer
   source still uses ML-KEM-768, and coordinator/Apple-managed TLS and identity
   are separate paths. Those require versioned compatibility migrations and
   cannot be relabeled based on this VPN change.
9. Integrate signed reproducible release packaging and staged rollback. Normal
   packaging still embeds stock NetBird and would replace a local experiment.

[NIST FIPS 203](https://csrc.nist.gov/pubs/fips/203/final) defines ML-KEM and its
parameter sets. [FIPS 204](https://csrc.nist.gov/pubs/fips/204/final) separately
standardizes post-quantum signatures. Neither is product-wide certification of
this application. Category 5 in the UI describes ML-KEM-1024 parameters only.

## Historical installations (before the gated TCP candidate)

## Authorized local installation — 2026-09-27

The experimental runtime was built for macOS arm64 and amd64, combined into a
universal executable and ad-hoc signed (not Developer ID notarized). It replaced
only `/Applications/neXal-Connector.app/Contents/Helpers/nexal-network` on this Mac.
The existing launchd job `system/netbird` was restarted through the macOS
administrator prompt. Both CLI and live daemon then reported
`0.79.0-nexal-mlkem1024-experimental`, and management connectivity was healthy.

The previous runtime and SHA-256 manifest are retained at:
`/Users/kknott/.config/nexal/runtime-backups/20260927T055258Z/`.
Rollback consists of verifying the manifest's original hash, atomically restoring
`nexal-network-original` to the helper path, and restarting `system/netbird` with
administrator authorization. No credentials or networking configuration were
replaced. The existing peer inventory was two peers; one reported connected after
the restart. This is WireGuard connection status, **not proof of a completed
ML-KEM-1024 exchange**. Neither remote peer was upgraded by this installation.

All production gaps above remain open. This installation was explicitly requested
after disclosure of those gaps; it is not a statement that they were fixed.
Normal app repackaging still embeds upstream NetBird and would replace this local
experimental helper. The earlier strict ML-KEM-1024 application-TLS source change
was not installed by this helper-only runtime replacement.


### Evidence upgrade — 2026-09-27

The follow-up replaces both `nexal-network` and the Go `nexal` helper with
universal, ad-hoc-signed builds. The runtime version is
`0.79.0-nexal-mlkem1024-evidence-experimental`. Rollback copies of both helpers
and their SHA-256 manifest are in
`/Users/kknott/.config/nexal/runtime-backups/20260927T060821Z-evidence/`.
This also activates the previously prepared strict application peer-TLS changes.
The native Swift executable is unchanged.

Fresh zero-fuzz preparation and race tests passed for NetBird's Rosenpass,
peer status and status serialization packages. Connector mesh, agent, client
and CLI tests passed with `TestDevelopmentPrivateLoopEndToEnd` excluded because
its singleton lock conflicts with the owner's running Connector. Existing client
TLS tests were updated to inspect the observability-wrapped HTTP transport.

After the runtime restart, management was connected, two peers were connected,
and **zero peers reported installed ML-KEM evidence**. After relaunch, the running
Connector served local status with mesh `pq: degraded` and zero protected peers.
Remote peers were not
upgraded. The bootstrap/expiry traffic gate and MTU-safe transport remain open;
this update improves local evidence reporting and does not solve those gaps.

### Status probe network budget

`prepare.py` also applies `netbird-health-budget.patch`: concurrent status readers
share management health RPC results for 30 seconds after success, or 10 seconds
after failure. Live transport state, management Sync errors, peer statistics and
quantum evidence continue to be evaluated independently. This bounds background
health traffic without changing peer signaling, handshakes or key renewal.
The tradeoff is up to 30 seconds to detect a management application failure that
does not also break the transport or Sync stream. Run
`go test -race ./shared/management/client -run TestHealthProbe` in the prepared
NetBird tree to verify expiry, recovery and concurrent polling.
