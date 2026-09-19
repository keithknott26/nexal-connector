# Private pool implementation validation

Status: implemented and tested reusable package plus optional real authenticated
peer object transport. Integration API guide: `INTEGRATION.md` in this folder.

Validation on installed Go 1.26, Linux/amd64:

- `go test -race ./internal/pool -count=3 -cover` — PASS; 81.7% statement coverage
  in the recorded run, 20 top-level tests plus table subtests.
- `go vet ./internal/pool` — PASS.
- `gofmt -d internal/pool/*.go` — no formatting differences.
- `GOOS=darwin GOARCH=arm64 go test -exec /bin/true ./internal/pool` — compilation
  PASS. This is cross-compilation only, **not** macOS runtime/hardware validation.
- `go test -race ./...` from the connector module — PASS across cmd/nexal, agent,
  client, config, pool and tunnel in the recorded integration run.

The initial ErrUnsafePath failures were test setup, not relaxed path protection:
Go 1.26 `testing.T.TempDir` creates its per-test child with mode 0777 filtered by
umask; the strict store requires an explicitly private 0700 selected directory.
The test helper now chmods only its dedicated test directory to 0700. Production
store policy still rejects group/world-accessible roots and all symlink ancestors.

Covered invariants:

- Content SHA-256, exact stream sizes, corruption detected before emitting bytes,
  immutable existing copies, 0600 modes, exclusive store ownership.
- Quota rollback, failed free-disk probe, disk floor checks, cache-only eviction,
  persistent startup accounting, duplicate concurrent writes.
- Rejected traversal, symlink roots/objects, external hardlinks; pinned directory
  still writes safely after its original pathname is replaced with a symlink.
- One-use pinned-key Ed25519 invitation proofs, replay, fingerprint/role tampering,
  expiry, revocation, concurrent one-use enrollment.
- One receipt and duplicate copies from the same device cannot mark protection;
  explicit single-copy consent, two distinct signed confirmations, expiry,
  revocation, repair planning and missing-backup-source detection.
- Manifest version CAS/hash chain, retention tombstones, metadata backup damage
  and chain validation, idempotent catalog restore plus separate object restore.
- Combined public/private/MLX/storage concurrency, no over-admission, idempotent
  release, TTL expiry, epochs, owner reclaim, public opt-in, overflow rejection.
- All memory terms per MLX rank, pinned compatible runtimes, safety margin,
  per-rank rather than aggregate fit, base-M4 JACCL rejection and clique checks.
- Actual local mTLS upload/download, verified signed upload acknowledgment,
  pin mismatch, anonymous/nonallowlisted peers, live revocation over keepalive,
  fake HTTP 201 receipt rejection, remote corruption and quota failures, unsafe
  paths/endpoints, wildcard bind rejection and one-copy replication failure.

No claim: runtime enforcement, MLX execution/benchmarking, at-rest encryption,
physical-device attestation, production deployment, independent backup retention
or certification. Membership persistence/rotation remains an embedding-agent
responsibility. Source edits are confined to this folder; go.mod was not edited.
