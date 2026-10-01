# Bundled quantum-resistance profile

The connector packages NetBird **v0.79.0**, whose `go.mod` pins `cunicu.li/go-rosenpass v0.5.42`. That Rosenpass implementation uses **Kyber-512** for its ephemeral KEM and **Classic McEliece 460896** for its static KEM. This is not ML-KEM-1024 or Kyber-1024. No NIST Category 5 claim is supported for the current profile.

The audited source is `go-rosenpass` tag v0.5.42, commit `8a9c11cd2763c3c7396e2417ee74c6d69bdb33be`:

- [NetBird v0.79.0 dependencies](https://github.com/netbirdio/netbird/blob/v0.79.0/go.mod)
- [KEM imports and assignments](https://codeberg.org/cunicu/go-rosenpass/src/tag/v0.5.42/crypto.go): `kyber512.Scheme()` and `mceliece460896.Scheme()`.
- [Protocol identifier](https://codeberg.org/cunicu/go-rosenpass/src/tag/v0.5.42/protocol.go): Rosenpass v1 mceliece460896 Kyber512.

The upstream runtime exposes a quantum-resistance flag, not a negotiated algorithm identifier. Consequently, the customer-facing **Quantum Type** row says **Not reported**, even when the separate protection status says **Protected**. Help text describes the bundled profile as a build-time fact, not a handshake attestation. That mesh-label change did not alter polling or cryptographic behavior; the separate peer-transport upgrade below changes application TLS.

**ML-KEM-1024 — NIST PQC Category 5** is the requested target, not an enabled feature. Enabling it requires an explicit interoperable protocol implementation and verification across participating peers. Renaming Kyber to ML-KEM or increasing a display label would not deliver that protocol change.

## Peer application transport: ML-KEM-1024 implemented in source

The connector's `internal/pool` mutual-TLS transport now explicitly requires
`SecP384r1MLKEM1024` (TLS group 4589), combining P-384 ECDHE with standardized
ML-KEM-1024. Both client and server allow only this group and check the negotiated
group in the certificate-verification callback. This shared transport is used by
peer object transfers and collective communication. It does **not** replace the
NetBird/Rosenpass VPN, coordinator HTTPS, iOS URLSession, or Drive file envelopes.

Validation uses the actual peer client/server configuration, real local TLS
handshakes, and enrolled device identities. It verifies group 4589, rejects
classical X25519 and both ML-KEM-768 hybrids on either side, and confirms that
`GODEBUG=tlsmlkem=0,tlssecpmlkem=0` cannot weaken the explicit suite. From Go 1.26.x the
stdlib intersects `CurvePreferences` with the GODEBUG-gated defaults, so those settings would
leave no usable group (fail closed); `EnforcePostQuantumGODEBUG` strips them from the process
before any peer config is built. Go's explicit
`CurvePreferences` enables the listed supported groups; the earlier claim that
setting this field always disables PQ was incorrect.

Compatibility: peers without this hybrid fail to connect; there is no downgrade.
Existing Go 1.26 peers with normal default groups support it, but old peers that
pin ML-KEM-768, run earlier Go versions, or disable default hybrid groups need an
upgrade. Rollout requires a packaged build and mixed-version/device testing.
The authorized local evidence upgrade on 2026-09-27 installed the Go helper
containing this change; normal release packaging and remote rollout remain separate.

The ML-KEM component has NIST category 5 parameters. This does not establish
category 5 for the complete protocol/product: identity signatures remain Ed25519,
and TLS cipher selection has not been changed. A default mesh protection boolean
is not evidence of this application-transport handshake. UI mesh labels therefore
remain unchanged until mesh-specific evidence is available.

## Remaining VPN upgrade work

Normal release packaging still uses `Rosenpass v1 mceliece460896 Kyber512 ChaChaPoly1305
BLAKE2s`, including KEM-dependent packet layouts and persisted keys. Replacing a
KEM import or changing that name is not an interoperable, reviewed VPN upgrade.
A VPN migration must specify an authenticated, versioned protocol/profile,
persisted-key migration, strict treatment of unsupported peers, PSK installation
and rotation, and interoperability with the management/runtime distribution.
Validate replay, downgrade, reconnection, packet loss, expiry and mixed-version
behavior before enabling it on live machines. The existing mesh strict-PQ startup
remains in place while this work is unfinished.

References: [Go TLS configuration](https://pkg.go.dev/crypto/tls#Config),
[Go 1.26 release notes](https://go.dev/doc/go1.26),
[NIST FIPS 203](https://csrc.nist.gov/pubs/fips/203/final).

## Mesh integration candidate

An isolated, reproducible ML-KEM-1024 NetBird/Rosenpass candidate now lives in
[`experiments/mlkem1024-mesh`](../experiments/mlkem1024-mesh/README.md). Its tests
exercise real key exchange and renewal, but production bootstrap/expiry traffic
gating, broader profile evidence integration, MTU interoperability and protocol
review remain unresolved. Local per-peer installation and expiry evidence is now
carried through NetBird protobuf/JSON into Connector mesh status. Enablement flags
alone cannot establish protection. The running Connector was verified to report
zero protected peers when the runtime reported zero installed ML-KEM keys. It is intentionally absent from normal app packaging. An explicitly authorized
local experimental helper installation was activated on 2026-09-27; the remote
peers and native apps were not upgraded. See that document for the precise NetBird modifications.


## Gated-session candidate — not yet installed

The newer `nexal-mlkem1024-tcp-v2` candidate binds packet authorization to the
actual WireGuard traffic-key generation and its ML-KEM lease. It uses bounded
TCP control frames and passes an end-to-end two-device test at MTU 1280 with
bootstrap denial, protected traffic, expiry denial and renewal recovery. This
required changes to **NetBird, go-rosenpass and NetBird's WireGuard-Go fork**.
Connector source rejects earlier installation-only evidence. macOS UI source
labels the ML-KEM-1024 Category 5 parameter set only for fresh gated-session
reports. The previous evidence-only runtime remains the installed local version.

This still is not full product-wide Level 5 assurance. Remote M4/gateway rollout,
physical iOS integration, coordinator evidence propagation, authenticated key
binding, review of the modified protocol, and versioned migration of the Drive
ML-KEM-768 format remain. See the candidate README for the exact deployment and
validation boundary. Do not replace installed binaries with this candidate until
compatible peers and out-of-band recovery access are ready.

### Resilience changes — 2026-10-01 (source only)

The candidate's key exchange now backs off per peer after failed initiations, keeps a
peer-level lease expiry timer, uses direct/relayed delivery budgets, and only initiates toward
peers that advertise the `nexal-mlkem1024-tcp-v2` profile in signaling. Local status carries a
machine-readable reason (`pqReason`: `exchange-pending`, `peer-unreachable`,
`peer-lacks-profile`, `evidence-expired`, `key-install-failed`, `session-pending`,
`peer-disconnected`, `evidence-stale`, `runtime-not-strict`) so the apps can explain an
unprotected link. A device that never carries the profile (a phone) is reported as
`pq: unsupported` ("not covered") rather than as a degraded link; it still gets no sharing
services and its link stays unprotected. None of this relaxes the gate or changes the
Category 5 **parameter** claim, which remains per link, self-reported and experimental.
The coordinator report does not carry the reason yet. Nothing has been installed.
