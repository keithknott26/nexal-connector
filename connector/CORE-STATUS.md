# Connector core integration checkpoint

- `CLI-CONTRACT.md` is available for native UI integration.
- `go test -race ./cmd/nexal ./internal/config ./internal/client ./internal/agent ./internal/tunnel` passes on the installed Go 1.26 compiler.
- Module now requires Go 1.26 per integration lead's explicit instruction: pool uses secure `os.Root` operations, and CI/toolchain is Go 1.26. Core alone uses Go 1.23-compatible standard-library APIs, but the unified module must not claim Go 1.23 compatibility.
- Dev Linux end-to-end enrollment MUST use `enroll --code-stdin --dev-synthetic-hardware`, then `run --dev-private-pull --dev-assume-idle`. This deliberately registers simulated 8 GiB memory. Without the explicit flag, Linux Mac telemetry is unknown (0), and positive simulated heartbeats will correctly fail coordinator memory validation.
- `self-test --samples 1000000` provides useful owner-initiated offline CPU execution on a real Mac even when production remote dispatch is gated. It uses an exclusive lock, fixed bounded workload and five-second deadline; no cloud, grants, money or arbitrary code.
- Production pull execution is intentionally impossible. Incoming tunnel job endpoints reject all grants until Access JWT + signed-grant + independent verified dispatch is integrated. No QUIC log or self-report can unlock production execution.
- No installation, signing, native Mac runtime validation, root operation, boot persistence, deployment or external cloud action has been performed.
- Core race tests including full mock CLI → enroll → heartbeat → pull → CPU → complete passed. The pool owner corrected test-directory permissions without relaxing production checks; the final integrated `go test -race ./...` passes across all packages.
- Final core validation is saved in `build/core-validation.log`: all five core packages pass race tests; core `go vet` passes; Darwin arm64 cross-compilation and Linux build pass.
- Binaries: `build/nexal` (Linux test executable) and `build/nexal-darwin-arm64` (unsigned/notarization-unverified Mach-O arm64).
- macOS binary SHA256 at this checkpoint: `60d7da91a3243d58e003cd3b8ff730b8d84b26acd143bb97d8f247d62ed4e24d`.
- The integration lead also passed actual local Worker/D1 → Go connector execution, result, duplicate-safe metering, pause, cancellation and revocation checks. Real Mac and production cloud acceptance remain outstanding.
