# Native shell + MLX runtime handoff

## Completed scope

Only `macos/` and `runtimes/` source files were edited. No connector, root CI,
coordinator, deployment or repository operations were performed.

- `macos/`: macOS 14+ SwiftUI menu-bar executable Swift Package, XCTest contract
  tests, Info.plist, app-assembly script, build/sign/notarize/native QA instructions.
- `runtimes/nexal_mlx/`: lazy probe, strictly local pinned built-in llama inference,
  full-file integrity, offline tokenizer loading, fresh admission snapshot checks,
  complete dependency-receipt gate, fixed greedy generation limits.
- `runtimes/bridge/`: small **Go** module generating fixed isolated-Python argv
  and explicit trusted-LAN per-rank smoke plans. Go core retains all scheduling.
- Versioned workload manifests with SHA-256s; model/config/admission templates;
  complete-lock reproducibility gate rather than a fabricated Linux-resolved lock.
- Experimental ring collective test is only a scalar all-sum, not model
  partitioning or distributed inference. JACCL remains disabled, with explicit
  base M4/M2 rejection. No SSH/public listener/firewall provisioning.

## Exact core CLI used

Read and reconciled against `connector/CLI-CONTRACT.md` and current `cmd/nexal/main.go`:

```text
init --coordinator URL --name NAME --memory-limit-mib N --reserve-memory-mib N --config PATH
enroll --code-stdin --config PATH
run --config PATH
status --config PATH
pause --config PATH
resume --config PATH
```

Enrollment is stdin-only; max 255 code bytes plus newline fits Go's 256-byte input
bound. UI status requires top-level boolean `paused`, reads real current Go
fields and optional telemetry, and ignores future fields. Go secrets stay in the
Keychain. The shell does not mutate config directly. Resource toggle is private
resume/pause; public/cloud contribution is honestly gated, not a fake toggle.

## Test evidence from this Linux environment

Final Python command:

```bash
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=runtimes \
  python3 -m unittest discover -s runtimes/tests -v
```

**34 tests passed**, including complete dependency receipt equality and all
workload source hashes. `python3 -I runtimes/nexal_mlx_entry.py probe` returned
Linux/x86_64, Python 3.14.3, no MLX packages, backend not imported and distributed
execution disabled. No inference or network collective was executed.

Go bridge: `go test ./...`, `go test -race ./...`, and `go vet ./...` passed;
final formatting is clean and the race/vet checks were rerun after formatting.
Four named Go tests cover multiple cases.
Packaging script `bash -n` passed; Info.plist parsed successfully with stdlib.
No Swift compiler is available here, so **Swift build/XCTest, actual UI behavior,
Go Keychain integration, signing/notarization and native MLX tests remain unrun**.

## Parent integration actions / caveats

1. Add `runtimes/bridge` Go tests to root CI (separate small module).
2. The formerly missing `manifests/mlx-local-text-v1.json` now exists and its
   integrity test passes. Keep `PYTHONPATH=runtimes` for root Python test invocation.
3. Native CLI commands exactly match the Go core at handoff. Ship them together.
4. No runtime dispatch hooks were inserted into Go core. Current HTTP contract
   only accepts Monte Carlo; MLX registry entries remain dispatch-disabled.
5. `requirements.in` is candidate top-level pins; **not a full lock**. The
   complete dependency inventory and hardware receipt gate deliberately blocks
   inference until `RELEASE-GATES.md` is satisfied.
6. No local model, second-Mac inventory, numerical/performance result or
   production readiness is invented.
7. SDK research evidence files `runtimes/pplx_sdk_*.json` were retained under the
   workspace non-deletion rule and excluded by `runtimes/.gitignore`. They are
   not release dependencies; omit them from release bundles.
