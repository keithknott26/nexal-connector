# Approved local MLX runtime

Qwen3/Phi-4-mini support and the sequential champion–challenger API are
documented in [QWEN-PHI.md](QWEN-PHI.md). Runtime version: 0.2.0.


Go remains the primary implementation: [`bridge`](bridge) constructs fixed local
argument arrays; the connector's `internal/pool` owns memory admission,
reservations, placement, peer authorization and fencing. Python is only the
small MLX library adapter that Go cannot replace without changing the MLX API.

**Native tiny-fixture validation only.** No full checkpoint, production Mac lock, signed runtime,
performance claim, distributed model shard, or production MLX dispatch is shipped.
The coordinator's only enabled template remains `monte-carlo-pi-v1`.
The source manifest is a local runtime registry candidate, not authorization
to submit a new template to the coordinator.

## Available paths

| Path | Behavior |
|---|---|
| `probe` | Metadata-only, stdlib-safe, no MLX import unless `--load-backend` |
| `verify-model` | Check the owner-pinned local manifest and every model file |
| `infer` | Gate on Mac/complete dependency receipt, verify local model, check a fresh local Go admission snapshot, then deterministic single-host inference |
| `ring-smoke` | Explicit experimental opt-in only; fixed scalar all-sum on 2–8 owner-approved RFC1918 peers; **not distributed inference** |
| `bridge.PlanRingSmoke` | Generate deterministic pinned hostfile and per-rank argument arrays; no process execution, SSH or firewall changes |
| `pool.PlanMLX` (Go core) | Model-specific memory placement; weights/KV/activations/buffers/safety and per-rank headroom, not sum-of-installed-RAM arithmetic |

## Test without MLX or network

From repository root:

```bash
cd runtimes
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -v
python3 -I "$PWD/nexal_mlx_entry.py" probe
(cd bridge && go test ./...)
(cd bridge && go vet ./...)
```

Tests cover corrupt/missing/unpinned files, immutable revisions, remote-code
metadata, arbitrary Python, symlinks, mode checks, duplicate JSON keys,
non-finite values, memory/load peaks, stale/expired/future headroom, owner reserve,
model binding, lazy imports, argument allowlisting, isolated entrypoint,
LAN-only endpoints, duplicate ranks, opt-in and base-M4 JACCL rejection.
Fixture bytes are deliberately **not an executable MLX model**.

## Local provisioning (owner operation, never a remote job)

1. Complete [`RELEASE-GATES.md`](RELEASE-GATES.md). Keep the reviewed runtime
   directory and environment owner-controlled, not writable by other users.
2. Obtain a licensed, reviewed **built-in `llama`, `qwen3`, or supported `phi3` architecture** model separately.
   The runtime never downloads a model, repository or Python script. The model
   must be entirely local with regular safetensors weights and local tokenizer.
3. Create `nexal-model-manifest.json` in the model directory from
   `examples/model-manifest.template.json`. Replace all placeholders with real
   immutable revision, license, SHA-256 per file, and measured memory estimates.
   Every file in that directory must be declared. No `.py`, pickle, adapters,
   unlisted files, subdirectories, symlinks or model-provided external paths.
   Sanitize unwanted `_name_or_path`/`auto_map` metadata **before** hashing, under
   owner review; do not alter a pinned snapshot afterward.
4. Pin the SHA-256 of the manifest in an owner-managed mode-0600 runtime config.
   The model directory must not be a user-supplied remote-job field. Example
   placeholders intentionally fail validation.
5. The Go supervisor must reserve memory in its **single shared admission
   authority**, produce a mode-0600 per-attempt snapshot matching the example,
   and supervise the process group with a deadline, lease fencing and owner-stop
   cancellation. These worker dispatch hooks are **not integrated yet**.

The adapter checks worst-case allowed prompt KV memory before loading. This is
an estimate and defense-in-depth check, not an OS memory hard limit. Load peak,
resident weights, per-token KV, activations, temporary/communication buffers and
explicit safety margin must all be measured. A fresh snapshot is not a signed
remote grant and does not itself prevent double reservation or mid-run reclaim.

After explicit local release approval:

```bash
/ABSOLUTE/APPROVED/VENV/bin/python3 -I /ABSOLUTE/APPROVED/nexal_mlx_entry.py \
  verify-model --config /ABSOLUTE/OWNER/runtime-config.json

/ABSOLUTE/APPROVED/VENV/bin/python3 -I /ABSOLUTE/APPROVED/nexal_mlx_entry.py \
  infer --config /ABSOLUTE/OWNER/runtime-config.json \
  --admission /ABSOLUTE/OWNER/attempt-admission.json \
  --prompt-file /ABSOLUTE/OWNER/prompt.txt --max-tokens 128
```

The CLI has no `--model`, remote URL, module/script, adapter, trust-remote-code,
arbitrary generation kwargs, or shell escape. Token limit is 1–512, UTF-8 prompt
is at most 16 KiB and 4096 tokens; sampling is fixed greedy (`temp=0`).
The adapter never evaluates a model's Jinja chat template. The local loading
interface and safe tokenizer options follow the upstream API; current `main`
is not evidence the candidate pinned environment has passed compatibility tests
([MLX-LM loading implementation](https://raw.githubusercontent.com/ml-explore/mlx-lm/main/mlx_lm/utils.py)).

HF offline flags and explicit local paths prevent intentional downloads, but
environment flags are **not a network sandbox**. Use reviewed, offline-only
dependencies and test with outbound networking denied. Model-file hashing is
not a defense against a malicious owner changing files between verification and
load; use an immutable/read-only snapshot and a trusted host. No secret model
protection from participating hosts is claimed.

## Experimental trusted-LAN rank test

The Go private-pool planner selects the memory placement. `bridge.PlanRingSmoke`
only converts already-enrolled, explicitly trusted peer inventory into a small
collective test, and requires `experimentalTrustedLAN=true`. It returns exact
hostfile **bytes** and their hash, plus per-rank argument arrays. Write those
bytes without appending a newline to the owner-approved path on each peer,
mode 0600. The owner starts each command locally on the specified Mac.

Commands call only the bundled fixed `ring-smoke` entrypoint. They do not use
`mlx.launch` or SSH, discover peers, open public ports, alter the firewall,
enable RDMA or ask for root. They bind explicit private IPs supplied to MLX,
not `0.0.0.0`. The data path is not authenticated/encrypted by Cloudflare Tunnel.
Test only on an isolated owner-controlled LAN with explicit per-peer firewall
allow rules, no port forwarding, no customer data, and existing authenticated
control-plane pairing. Private address ranges alone do not establish trust.

This performs one integer collective and self-exits after 30 seconds if blocked.
The Go supervisor should additionally enforce a 35-second process-group deadline
and cancel every rank on any failure. It does not load a model, pool OS RAM,
partition weights, prove numerical inference correctness, or establish speedup.
The raw MLX rank and hostfile mechanism is documented by
[MLX distributed communication](https://ml-explore.github.io/mlx/build/html/usage/distributed.html).

**Backend policy:** use an explicit `ring`, never `any` or a singleton fallback.
JACCL launch remains disabled for this runtime. Standard M4 (and base M2) are
not valid Thunderbolt-5 JACCL peers. Apple's standard M4 mini has Thunderbolt 4;
the documented JACCL route requires Thunderbolt 5, macOS 26.2+ and fully direct
peer connectivity ([Apple specifications](https://support.apple.com/en-us/121555),
[MLX requirements](https://ml-explore.github.io/mlx/build/html/usage/distributed.html)).
Even an eligible M4 Pro is not certified by this implementation.

## Integration boundaries still open

- Coordinator MLX template registration, signed grants, scheduling and result API.
- Go process-group supervisor integration, refreshed reservations and reclaim tests.
- Publisher-authenticated workload/model registry, safe dependency updates.
- Hardware-tested model architecture/quantization, memory estimates and lock.
- Shared cluster two-phase reservation/commit hooked to actual runtimes.
- Supported model partitioner and per-rank loader avoiding full-weight peaks.
- Encrypted/authenticated distributed data plane and owner peer pairing enforcement.
- Measured M4/M2 numerical correctness, throughput, memory pressure and failure recovery.

No runtime code calls a connector service, spends money, initiates cloud fallback,
or starts an Internet-visible daemon.
