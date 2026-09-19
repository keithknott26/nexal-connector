# Nexal Connector continuation checkpoint

September 19, 2026. This is a private engineering preview, not a production
marketplace connector. Preserve all release gates and both repositories' privacy.

## Start here

- [Connector verification](VERIFICATION.md).
- [Connector security boundaries](SECURITY.md).
- [Rename and configuration migration](MIGRATION.md).
- [Go CLI contract](connector/CLI-CONTRACT.md).
- [Native UI setup](macos/README.md).
- [Private-pool integration](connector/internal/pool/INTEGRATION.md).
- [MLX release gates](runtimes/RELEASE-GATES.md).
- [Platform's complete product archive](https://github.com/keithknott26/nexal-platform/tree/main/docs).
- [Canonical cross-repository handoff](https://github.com/keithknott26/nexal-platform/blob/main/docs/CURRENT-HANDOFF.md).

## Immediate acceptance work

Latest source increment adds `nexal policy` and `nexal set-policy` through the
authenticated loopback API. Resource-policy updates persist atomically, cancel
active work and invalidate prior observations without enabling public execution.
Consent generations and per-observation sequences reject stale/out-of-order
telemetry and heartbeat completions. A native SwiftUI settings form is not added.
106 named Go test passes (63 top-level), race/vet, repeated race tests and a Darwin
ARM64 cross-build passed locally. The shared contract now documents additive
usage-budget configuration and bounded MCP protocol behavior.
The platform's `scripts/verify-local.sh` checks both sibling repositories and can
run the actual CLI/Worker integration using `--integration-port 8787`.

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
