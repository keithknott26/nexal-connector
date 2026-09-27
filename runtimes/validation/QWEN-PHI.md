# Qwen/Phi validation — 2026-09-27

Source implementation: runtime 0.2.0. Validation used the pinned MLX 0.29.3,
MLX-LM 0.28.4, Transformers 4.57.6 and Tokenizers 0.22.2 on Apple-silicon macOS
27.0 with CPython 3.11.15. The native test uses tiny RANDOM weights with the
actual official Qwen3-4B and Phi-4-mini-instruct tokenizers, not trained models.

- 41 Python unit/contract tests passed.
- Go supervisor/comparison and CLI suites passed with `go test -race ./...`.
- `go vet ./...` passed.
- Four native Metal cases passed: Qwen3 and Phi3, each float and affine 4-bit.
- Official instruction templates and exact token IDs matched.
- Save/load logits matched exactly; cached/uncached maximum error < 0.000003.
- Greedy output repeated deterministically for all four cases.
- No real checkpoint quality, real checkpoint peak memory or training claim.

`qwen-phi-native-smoke.json` contains native results; tokenizer provenance is adjacent. The test initially aborted inside sandboxed Metal
initialization; the successful run had host GPU access and used local fixtures
only. A first stalled native process was terminated, and the successful rerun
used a diagnostic timeout. Transformers' misleading Mistral-regex warning on
our synthetic configs was resolved by preserving the official config's
transformers_version; token IDs were also independently compared against the
original tokenizer.json implementation. No tokenizer rewrite was applied.

The full checkpoints were not downloaded: available disk space was about
3–4 GiB, insufficient for both model snapshots, staging and validation headroom.
The production flags remain false. A full immutable environment lock, real
checkpoint conversion/load verification, memory/latency calibration, labeled
cybersecurity evaluation and host-service dispatch are remaining release tasks.
