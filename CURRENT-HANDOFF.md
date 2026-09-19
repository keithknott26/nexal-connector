# Nexal Connector continuation checkpoint

September 19, 2026. This is a private engineering preview, not a production
marketplace connector. Preserve all release gates and both repositories' privacy.

## Start here

- [Connector verification](VERIFICATION.md).
- [Connector security boundaries](SECURITY.md).
- [Cloudflare/cloudflared trust and deployment status](CLOUDFLARED-TRUST.md).
- [Rename and configuration migration](MIGRATION.md).
- [Go CLI contract](connector/CLI-CONTRACT.md).
- [Native UI setup](macos/README.md).
- [Private-pool integration](connector/internal/pool/INTEGRATION.md).
- [MLX release gates](runtimes/RELEASE-GATES.md).
- [Platform's complete product archive](https://github.com/keithknott26/nexal-platform/tree/main/docs).
- [Canonical cross-repository handoff](https://github.com/keithknott26/nexal-platform/blob/main/docs/CURRENT-HANDOFF.md).

## Immediate acceptance work

Native update: owner-supplied M4 output for connector `76345d9` confirms Go
vet/race package tests, Swift compilation, seven XCTest cases and initial app
packaging passed. Read MAC-ACCEPTANCE.md before repeating build-only checks.
App launch, Keychain, telemetry and recovery are still pending. The Swift local
preview is fixed to port 8787, which an existing Docker service occupies;
identify that service before stopping it or attaching the app.

Latest installation pass adds `nexal doctor` / `doctor --probe`, a sanitized
read-only configuration/optional telemetry report. No credentials or network
connections are accessed. Native app packaging stages a complete replacement
before publishing it, with locks and preservation of the prior app on ordinary
failure. Power-loss recovery and successful packaging still need real Mac tests.
The platform supplies easier double-click setup/start, a supervised readiness
launcher and installer fault-injection tests. See its INSTALLATION-BUGCHECK.md.

Latest source increment adds `nexal policy` and `nexal set-policy` through the
authenticated loopback API. Resource-policy updates persist atomically, cancel
active work and invalidate prior observations without enabling public execution.
Consent generations and per-observation sequences reject stale/out-of-order
telemetry and heartbeat completions. A native SwiftUI settings form is not added.
190 named Go test passes (89 top-level), race/vet, repeated race tests and a Darwin
ARM64 cross-build passed locally. The shared contract now documents additive
usage-budget configuration, bounded MCP protocol behavior and the new owner-only
transactional audit endpoint. Coordinator migrations through 0004 are required.
The platform's `scripts/verify-local.sh` checks both sibling repositories and can
run the actual CLI/Worker integration using `--integration-port 8787`.

The latest safety pass adds an independent stale-observation reclaim watcher,
pending-pull cancellation fencing, bounded network contexts, strict JSON
ambiguity checks and safer private-file reads. Saved configuration must include
explicit, non-null `paused`; generated configurations already comply.
Tunnel diagnostics remain local observations, never verification or attestation.
See the platform's `docs/PRODUCTION-HARDENING-PASS.md` for the full integrated
report. Its 424 TypeScript tests and 62 installer/startup checks passed locally.

The owner has an M4 Mac mini (10 cores, 24 GB RAM, 512 GB) and an M2 Mac.
Compile and test the SwiftUI application, Keychain integration, setup scripts and
container coordinator on real macOS before claiming native acceptance. The Go
Darwin ARM64 cross-build passed, but it does not validate those native components.
New Nexal state is separate from the old KWK install; do not automatically copy
or delete old credentials. Stop older services using ports 8787/8788 first.

Production dispatch remains fail-closed. Distributed MLX, proven malicious-code
isolation, actual PQ negotiation and signed/notarized distribution are not
complete. Private-pool primitives are not automatically a fully integrated
filesystem or transparent shared OS memory.

## External status

The owner purchased `nexal.systems`; no domain deployment or DNS changes were
performed. Cloudflare account inspection is blocked by a repeated
`Invalid format for X-Auth-Key header` error. Resolve credential type without
requesting secrets in chat. GitHub Actions remains disabled pending review.
The platform repository preserves business documents, financial models, four
original flowcharts, dashboard source and screenshots, and the full architecture.

The owner's explicit next-session resume point is to finish DNS for
`nexal.systems` and link Claude with Cloudflare. Neither is complete. Confirm
which Claude client and whether the desired connection is developer/infrastructure
access or end-user access to Nexal's MCP before granting permissions.

Public registration evidence does not establish who owns `cloudfare.com`;
the registrar redacts registrant identity. No spelling-variant publisher was
trusted and no live tunnel was enabled. Preserve the owner's dirty
`cloudflare-staging` worktree when updating; do not reset or overwrite it.
