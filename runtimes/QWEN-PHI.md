# Qwen / Phi champion–challenger runtime

Runtime 0.2.0 adds explicit built-in `qwen3` and `phi3` support. The intended
initial pair is Qwen3-4B as champion and Phi-4-mini-instruct as shadow challenger.
That assignment is an evaluation starting point, not a measured accuracy winner.
The legacy `llama` raw-text profile remains available.

## What is implemented

- Architecture-bound tokenizer allowlists: Qwen2 fast tokenizer for Qwen3;
  GPT2 fast tokenizer for Phi-4-mini; both may use PreTrainedTokenizerFast.
  Qwen/Phi require a pinned local tokenizer.json. No custom Python or remote code.
- Qwen's explicit head dimension and Phi's partial rotary/LongRoPE configuration
  validation; malformed or unsupported configurations fail closed.
- Fixed, code-owned instruction formatting. Qwen3 uses the official non-thinking
  suffix; Phi uses its official role delimiters. Model-provided Jinja is not
  evaluated during inference. Reserved role delimiters in input are rejected.
- Atomic special-token checks. Phi stops on both `<|end|>` and `<|endoftext|>`;
  upstream config.eos_token_id alone omits its end-of-turn token.
- Existing immutable model/file pins, local-only load, no adapters, full
  dependency receipt, memory admission, lease deadline and process-group controls.
- `bridge.RunChampionChallenger`: sequential supervised processes, fresh
  model-bound admission per role, release before acquiring the next reservation,
  distinct attempt IDs, common bounded prompt and token budget, input digest,
  and preservation of the champion result if the shadow run is unavailable.
  Either reviewed family can be champion; identical families/digests are rejected.
  No automatic promotion, action authority, model downloads or weight training.

## Provisioning each real checkpoint

The model types are `qwen3` for Qwen3-4B and `phi3` for Phi-4-mini-instruct.
Use a separately reviewed, licensed local safetensors checkpoint compatible with
MLX-LM 0.28.4; a model name alone does not validate the weights or conversion.
Keep a separate directory, manifest, runtime config and owner policy per model.

The official Phi configuration contains `auto_map` and `_name_or_path`.
An operator must prepare a reviewed local snapshot without those remote-code
metadata fields or bundled Python, and record both upstream revision and the
local transformation before hashing it. Do not weaken the runtime's rejection
or switch on `trust_remote_code`. Only include the allowlisted model/tokenizer
files. The runtime never sanitizes or silently changes an already pinned model.
The official GPT2Tokenizer metadata is supported through the built-in fast
implementation and tokenizer.json; no external tokenizer repository is loaded.

Set `model_type` in the existing schema-1 model manifest and provide measured
memory terms for EACH quantization. Both models retain the runtime's 4096 input
and 512 output token limits, regardless of their advertised context capacity.
Four-bit weights do not mean four-bit KV memory. Re-hash the runtime source tree,
config and owner policy for version 0.2.0; older Go supervisors deliberately
reject the new runtime version rather than accepting an unreviewed update.

The native fixture evidence validates float and 4-bit paths with synthetic tiny
weights and the official tokenizers. It is not a production model release
receipt or a benchmark of either full checkpoint. RELEASE-GATES.md still applies
before deployment: full checkpoint loading, numerical/reference checks, measured
memory/latency, complete locked environment and cybersecurity evaluation.

## Integration

Single-model inference uses the existing CLI unchanged:

```text
nexal-mlx-job --owner-policy /owner/qwen-policy.json \
  --admission /owner/fresh-qwen-admission.json --prompt-file /owner/evidence.txt \
  --attempt-id EXISTING_ATTEMPT --model-manifest-sha256 REVIEWED_DIGEST
```

Host-service integration uses the new Go function:

```go
result, err := runtimebridge.RunChampionChallenger(ctx, runtimebridge.ComparisonPlan{
    Champion: runtimebridge.Candidate{Owner: qwenPolicy},
    Challenger: runtimebridge.Candidate{Owner: phiPolicy},
    PromptPath: immutablePromptPath,
    MaxTokens: 256,
}, acquireFromExistingAdmission)
```

The trusted local callback receives role and model digest, reserves against the
existing shared pool admission authority, creates a fresh snapshot, and returns
`LocalJob` plus a nonnil release function. On acquisition error it cleans up any
partial reservation itself. Return distinct attempts. It must enforce owner
reclaim/cancellation as in the single-model integration. The wrapper does not
mint grants or promise available RAM. Do not pre-create both snapshots: the
second would usually be stale by the time the champion finishes.

Use an immutable/read-only prompt snapshot; the comparison also checks its bytes
before and after each run. This is not protection against a malicious local
owner changing and restoring files while a process reads them. Keep outputs
private and tenant-scoped. The returned primary and shadow answers are candidates
for evaluation, not verified facts. Production sensor/chat dispatch remains an
integration task; this API is a working local orchestration boundary.

For promotion, compare held-out cyber cases with independently labeled expected
outcomes: missed threats, false positives, grounded references, abstention,
injection resistance, latency and memory. Agreement between models is not
accuracy. Record dataset/checkpoint/runtime versions, review the proposed role
change, retain the prior pinned champion, and re-run regression tests. OVH/Mythos
can review disagreements later; neither is called by this local runtime.

## Reproducing validation

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -v
(cd bridge && go test -race ./... && go vet ./...)
# Explicit Metal lab; already downloaded, revision-pinned official JSON only:
/path/to/lab/python -I -B tests/native_smoke.py --tokenizers-root /lab/tokenizers
```

The native script creates only temporary, random-weight fixtures, verifies
local manifests, compares fixed formatting with the official tokenizer templates
in the lab, checks reload and cache equivalence, and repeats greedy generation.
It requires Metal access and fails if unavailable. It does not set any production
approval flag, create a deployment receipt, or perform downloads.
