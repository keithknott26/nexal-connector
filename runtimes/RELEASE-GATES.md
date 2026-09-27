# Reproducible MLX environment gate

## Current state: full checkpoint release validation still required

Runtime 0.2.0 supports Qwen3 and Phi-4-mini explicitly. See
[QWEN-PHI.md](QWEN-PHI.md) for the model preparation and native fixture checks.
Tiny synthetic weights do not satisfy real-checkpoint accuracy, memory or
performance release gates. Keep production approval false until those pass.


`requirements.in` pins **candidate top-level versions only**:
`mlx==0.29.3`, `mlx-lm==0.28.4`, and `transformers==4.57.6`. These releases exist; that does not prove their
combination works on every macOS/Python target
([MLX 0.29.3](https://pypi.org/project/mlx/0.29.3/),
[MLX-LM 0.28.4](https://pypi.org/project/mlx-lm/0.28.4/)).
No complete transitive lock was fabricated on Linux. Inference and network
smoke are blocked until the owner supplies a reviewed, hash-pinned receipt of
the **complete installed environment** matching the Mac/Python target.

## Release engineer workflow

Use an Apple-silicon Mac, a clean operator-owned workspace, and a specifically
recorded CPython patch version, OS build, Xcode version and hardware inventory.
Do not modify the system Python or run installation commands submitted by jobs.
Resolve in a disposable build environment; review every transitive package and
wheel before installation. Do not permit editable installs or source distributions.

Example commands (run from `runtimes`; choose and record your `python3.11`):

```bash
python3.11 --version
sw_vers
uname -m
python3.11 -m venv .venv-lock
.venv-lock/bin/python -m pip install 'pip==25.0.1' 'pip-tools==7.4.1'

# Full platform-specific dependency graph + hashes. Resolve only wheels.
.venv-lock/bin/pip-compile --generate-hashes \
  --pip-args='--only-binary=:all:' \
  --output-file=requirements.macos-arm64.lock requirements.in

# Pin bootstrap tools too; include these versions in your reviewed runtime
# lock/receipt if they are installed in the runtime environment.
cat requirements.macos-arm64.lock
.venv-lock/bin/python -m pip download --only-binary=:all: --require-hashes \
  -r requirements.macos-arm64.lock --dest wheelhouse
shasum -a 256 requirements.macos-arm64.lock
shasum -a 256 wheelhouse/*.whl

python3.11 -m venv .venv-runtime
.venv-runtime/bin/python -m pip install --no-index --find-links=wheelhouse \
  --only-binary=:all: --require-hashes -r requirements.macos-arm64.lock
.venv-runtime/bin/python -m pip check
.venv-runtime/bin/python -m pip freeze --all
```

The freshly created venv may contain `pip`/`setuptools` not listed in the
application resolver output. Either remove them from the final immutable runtime
after provisioning, or acquire/version/hash their exact wheels and include them
in the complete release inventory and bootstrap lock. Do not omit installed
packages from the receipt. Avoid mixing tooling-only packages into the runtime.
The resolver tooling environment is distinct and is not trusted as a runtime lock.

**Archive as one reviewed release:** CPython installer checksum, all wheels,
wheel hashes, full requirements lock (including bootstrap policy), `pip check`
output, licenses, package metadata, target platform tags, workload source
manifest, model manifest, smoke/benchmark evidence, and an operator approval.
Recreate from the archived wheels **offline** and compare the complete inventory.
Never accept a lock produced by a remote workload as authorization.

## Dependency receipt format

This is an owner-managed gate, not remote attestation or a cryptographic signature.
Write mode 0600, then hash the receipt bytes into `runtime-config.json`.
All package names use lowercase normalized hyphens. Include every distribution
visible to the isolated interpreter, not merely the top-level packages.

```json
{
  "schema_version": 1,
  "macos_hardware_smoke_passed": false,
  "python_version": "EXACT_REVIEWED_PATCH_VERSION",
  "packages": {
    "mlx": "0.29.3",
    "mlx-lm": "0.28.4",
    "transformers": "4.57.6",
    "EVERY_OTHER_INSTALLED_DISTRIBUTION": "EXACT_VERSION"
  },
  "requirements_lock_sha256": "SHA256_OF_COMPLETE_REVIEWED_LOCK",
  "wheel_sha256": {
    "mlx": "SHA256_OF_EXACT_TARGET_WHEEL",
    "mlx-lm": "SHA256_OF_EXACT_TARGET_WHEEL",
    "transformers": "SHA256_OF_EXACT_TARGET_WHEEL",
    "EVERY_OTHER_INSTALLED_DISTRIBUTION": "SHA256_OF_EXACT_TARGET_WHEEL"
  }
}
```

Leave `macos_hardware_smoke_passed` false until the selected upstream load,
tokenizer, greedy stream generation and scalar ring API (if requested) have
been exercised in the disposable local lab using the exact frozen environment,
licensed fixture model, and no customer data. Record evidence and only then
approve the receipt and test the complete adapter. A boolean alone is not proof:
the release operator must retain the referenced evidence. A successful upstream
test does not waive the adapter-specific gates below.

Runtime checks require:

- Apple-silicon Darwin interpreter.
- Exact CPython patch version, exact complete installed package inventory, and
  the three candidate versions.
- Reviewed receipt digest, a full lock digest, and one wheel hash per package.
- An explicit recorded Mac hardware approval.

The receipt validates inventory and records wheel hashes; it does not rehash
installed wheel contents on every request. Seal/verify the installed environment
as part of packaging and prevent same-user tampering operationally. Integrity
checks do not create a malicious-process sandbox.

## Required native integration evidence before dispatch

1. Import/probe Metal, verify local-only tokenizer behavior and exact API
   signatures against the frozen releases; fail rather than auto-upgrading.
2. With outbound networking denied, load one known model locally and compare
   greedy outputs against a fixed reference. No unreviewed model downloads.
3. Measure warm/cold time, peak resident/load memory, KV growth, power, swap and
   owner interactivity on the actual M4 mini and actual second Mac.
4. Calibrate conservative model manifest memory terms and verify rejection when
   any rank or local process exceeds owner allocation/headroom.
5. Integrate Go lease snapshot creation and process-group cancellation; test
   pause, owner activity, sleep, low-memory pressure, expired leases and Go crash.
6. For ring smoke: explicit authenticated peer control, restricted LAN interfaces,
   reviewed ports, timeout/failure cleanup. No SSH or router forwarding.
7. Keep distributed **model** inference disabled until a specific partitioner
   demonstrates per-rank memory, reference correctness and all-rank cancellation.
8. Publish the lock, evidence and supported matrix before enabling coordinator
   dispatch; change the source manifest/version whenever runtime code changes.
