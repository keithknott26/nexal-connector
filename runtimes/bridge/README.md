# Private, single-host MLX job supervisor — bounded first stage

This standalone Go module now executes the **existing** Python MLX adapter as a
supervised, one-shot local process. It does not implement a scheduler, expose a
service, or make distributed model inference available. This is not transparent
RAM pooling and is not a production-ready dispatch path.

## What runs

`RunLocalInference(ctx, ownerPolicy, localJob)` is the library entry point.
`cmd/nexal-mlx-job` is a small CLI around the same entry point. Actual inference
is gated to **Darwin arm64**. There is no public platform override, command
override, environment override, shell, installer, downloader, SSH command,
network listener, or remote execution API.

The command is built exclusively by `InferenceCommand`:

```text
/owner/pinned/python -I -B /owner/release/nexal_mlx_entry.py infer
  --config /owner/config.json
  --admission /owner/existing-admission.json
  --prompt-file /owner/prompt.txt
  --max-tokens 128
```

These are separate argv elements, not a shell string. `-I` isolates Python's
environment/import behavior; `-B` prevents creating mutable bytecode in the
reviewed source tree. The adjacent `nexal_mlx` package is still imported by the
reviewed entry point. The shared command builder also adds `-B` to its existing
ring-smoke *plans*; this runner never executes those plans.

## Required owner-managed deployment

Before invoking the CLI, the owner must already have all of:

1. A reviewed Apple-silicon Python environment, installed through the existing
   release process. No installation is performed here.
2. A dedicated, immutable runtime-source directory containing **only** the
   reviewed `nexal_mlx_entry.py` and the `nexal_mlx` directory. Its files must be
   Python source, with no nested directories, symlinks, `__pycache__`, or
   unchecked sibling modules. Copying this repository's whole `runtimes`
   directory into that location does not satisfy this intentionally narrow
   deployment format.
3. Reviewed SHA-256 pins for the Python executable, entry point, config bytes,
   and **every source file** in that directory. The entry hash must be explicit
   and must match its entry in the complete tree manifest. The module never
   computes an approval from whatever code happens to be present.
4. The existing adapter's private runtime config, local reviewed model,
   complete dependency receipt, and release evidence. All existing Python
   model/dependency/platform/memory gates remain in force; Go validation does
   not bypass them.
5. A **pre-existing**, fresh, private admission snapshot with the expected
   attempt identity and model digest. This wrapper never creates or renews an
   admission, decides that memory is available, or authenticates its issuer.
6. A local nonempty UTF-8 prompt, at most 16 KiB, and an output limit of 1–512
   tokens.

All paths must be absolute and canonical, with no symlink components. Resolve
macOS aliases such as `/var` or `/tmp` to their canonical paths before using
them. The Python executable must be an actual regular executable at the pinned
path, not a venv symlink: provision an appropriate reviewed copy-based
environment rather than resolving a symlink and accidentally losing its venv.
Inputs must belong to the invoking user or root and must not be group/world
writable. The owner-policy file, config and admission must have no group/world
permissions (normally mode `0600`; read-only `0400` also works).

An **illustrative, nonfunctional** owner-policy file has this shape. Replace
every placeholder with a separately reviewed pin; do not use live self-pinning
as authorization. Keep policy/config/admission/prompt files outside the narrow
runtime-source directory.

```json
{
  "installation": {
    "python": "/opt/nexal/venv/bin/python3",
    "entry": "/opt/nexal/release/nexal_mlx_entry.py",
    "config": "/opt/nexal/private/runtime-config.json"
  },
  "entry_sha256": "REVIEWED_ENTRY_SHA256",
  "python_sha256": "REVIEWED_PYTHON_EXECUTABLE_SHA256",
  "config_sha256": "REVIEWED_CONFIG_SHA256",
  "runtime_files_sha256": {
    "nexal_mlx_entry.py": "REVIEWED_ENTRY_SHA256",
    "nexal_mlx/__init__.py": "REVIEWED_FILE_SHA256",
    "nexal_mlx/__main__.py": "REVIEWED_FILE_SHA256",
    "nexal_mlx/cli.py": "REVIEWED_FILE_SHA256",
    "nexal_mlx/runtime.py": "REVIEWED_FILE_SHA256",
    "nexal_mlx/security.py": "REVIEWED_FILE_SHA256",
    "nexal_mlx/ring.py": "REVIEWED_FILE_SHA256"
  }
}
```

The tree is bounded to 256 files / 16 MiB; the interpreter read is bounded to
128 MiB. Interpreter hashing does **not** seal its standard library, installed
packages, dynamic libraries, or the operating system. Release packaging must
protect and verify that complete environment.

## Build and invoke locally

From this standalone module, using an already installed Go toolchain:

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go build ./cmd/nexal-mlx-job
./nexal-mlx-job \
  --owner-policy /opt/nexal/private/owner-policy.json \
  --admission /opt/nexal/private/existing-admission.json \
  --prompt-file /opt/nexal/private/prompt.txt \
  --attempt-id EXISTING_ADMITTED_ATTEMPT \
  --model-manifest-sha256 REVIEWED_MODEL_MANIFEST_SHA256 \
  --max-tokens 128 \
  --timeout 2m
```

This example assumes an admission already issued under a real local reservation.
It intentionally contains **no command to fabricate an admission**. A hand-written
or replayed snapshot is not a memory reservation. The current Python contract
requires its observed headroom to be at most 15 seconds old and its expiry to be
within 300 seconds; this wrapper checks that contract as well.

On success stdout contains exactly one validated JSON result. Its schema,
template (`mlx-local-text-v1`), runtime version (`0.1.0`), attempt ID, model
manifest digest, and positive estimated memory requirement are checked. Required
memory must fit the snapshot's reservation and usable headroom. Duplicate,
unknown, missing and null fields, trailing JSON, invalid UTF-8, bad types and
identity mismatches fail closed. The generated text is intentionally returned
and may be sensitive; the caller is responsible for its destination.

On failure the CLI emits only a stable JSON error code to stderr, never an
OS exception, path, prompt, child stdout/stderr, traceback, or arbitrary adapter
message. Exit statuses: `0` success/help, `2` rejected input/policy/admission or
unsupported platform, `1` process/result/output-limit/busy failure, `124`
deadline, `130` cancellation. Raw child stderr is counted then discarded.

## Supervision and exclusion

- Stdout is capped at 1 MiB; stderr at 64 KiB. `--stdout-bytes` and
  `--stderr-bytes` can only reduce these limits. Zero selects the defaults.
  Exceeding either cap cancels and kills the process group; no partial result is
  returned.
- The effective deadline is the earliest of caller cancellation/deadline,
  requested timeout (maximum 300 seconds), and admission expiry. CLI SIGINT and
  SIGTERM cancel the same context. A library caller must cancel on owner
  reclaim, lease loss, pause, or memory pressure; none of those event sources
  is wired here.
- The child gets a new Unix process group. Cancellation, deadline and overflow
  kill the group with SIGKILL; the leader is waited for. Lingering descendants
  also receive group teardown after success/failure. A 250 ms `WaitDelay`
  prevents inherited output pipes from hanging completion.
- The child receives a fixed minimal environment with Hugging Face/Transformers
  offline flags, no inherited credentials, `PYTHONPATH`, `DYLD_*`, proxy
  configuration, or arbitrary application environment, and no input on stdin.
- A nonblocking exclusive `flock` is held on a securely opened, existing,
  private, read-only config descriptor through subprocess cleanup. A competing
  invocation using that same config inode returns `LOCAL_RUNTIME_BUSY` without
  launching. No lock files/PID files are created, no stale files are deleted,
  and release happens on descriptor close. Cancellation and failure release
  the lock.

**The lock is per-config only**, cooperative, and tied to the existing inode.
Different configs, replacement inodes, other launchers, or multiple users with
different deployments can still contend for the same physical memory. It is
not an authoritative cross-config/device scheduler reservation, a one-use
admission ledger, or replay protection. Do not replace a config in place while
jobs are running. Replaying an otherwise fresh snapshot after lock release is
not prevented here.

## Security and integration limits — do not skip

- **No network sandbox is provided.** Offline environment flags and the reviewed
  adapter's local-file-only behavior do not constitute an OS egress firewall.
  Use separately enforced outbound-denied native validation. Trusted installed
  Python/dependencies still execute with the invoking user's OS privileges.
- **Same-user file TOCTOU remains.** `O_NOFOLLOW`, canonical paths, descriptor
  checks, ownership/mode checks and digest verification reject many unsafe
  inputs, but Python later reopens paths. A writable parent, same-user attacker,
  privileged actor, or file replacement can invalidate the assumptions.
  An immutable/read-only, owner-controlled installation, stable config inode,
  protected parent directories, and immutable per-job input snapshots are
  deployment gates, not optional hardening. This is not a malicious-code
  sandbox.
- Process groups are supervision, not hostile-process containment. Deliberately
  escaped sessions/groups and a supervisor SIGKILL/crash are not solved here.
  Darwin parent-death cleanup, sleep/wake behavior and managed-service crash
  recovery still need a native supervisor/service design and tests.
- The Go coordinator/pool is **not integrated**. No reservation is acquired,
  signed, renewed or released; no live headroom is measured; no owner-activity
  observer, lease-loss stream, durable attempt ledger, retry or result upload
  is added. Admission files remain snapshots. Concurrent cross-config memory
  accounting and final reservation release after reclaim belong to the
  authoritative scheduler.
- Distributed inference stays disabled. There are no public jobs, network
  listeners, authenticated peer control, model sharding, remote-code jobs, or
  transparent RAM semantics in this change.

## Validation and remaining native evidence

Linux tests execute a compiled Go helper through an **unexported test-only
command factory**, not Python or MLX. They exercise real processes/process
groups, deadlines, caller cancellation, admission expiry, output bounds,
descendant teardown (including inherited-pipe hangs), exact argv, environment
isolation, sanitized errors, integrity/path/permission gates, result protocol
rejection, and config-lock contention/release. Test admissions are synthetic
fixtures only and are never issued by the production runner.

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -count=1 ./...
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -race -count=1 ./...
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go vet ./...
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 GOTOOLCHAIN=local \
  GOPROXY=off GOSUMDB=off go build ./cmd/nexal-mlx-job
```

The recorded Linux checks and Darwin arm64 **cross-compilation** do not prove
native behavior. **Actual MLX inference was not executed.** There is no
Mac-tested dependency lock/receipt or native performance claim created here.
Before enabling dispatch, retain native evidence for the frozen dependency
environment, no-egress local model load/output correctness, Metal behavior,
peak memory/swap/power/interactivity, signal and descendant teardown, advisory
locking on the actual filesystem, sleep/wake and supervisor crash cleanup.
Complete the existing Python release gates and integrate authoritative
admission/reclaim first.
