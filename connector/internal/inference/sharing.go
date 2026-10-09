package inference

import "fmt"

// Fixed statements about what the code base does and does not contain. They were
// established by reading: runtimes/README.md ("ring-smoke ... not distributed
// inference"; worker dispatch hooks "not integrated yet"), connector/internal/pool
// (placement.go, collective.go), connector/cmd/nexal/collective.go,
// connector/internal/privateruntime, connector/internal/client/private_runtime.go
// and runtimes/bridge. If multi-host execution is ever added, this file is the
// one place that must change, and TestSharingStatementsAreHonest must be updated
// deliberately.
var sharingEvidence = []string{
	"pool.PlanMLX (internal/pool/placement.go) only plans memory placement; every plan reports executionValidated=false and it never launches MLX.",
	"pool collective + `nexal collective` (internal/pool/collective.go, cmd/nexal/collective.go) is a ring all-reduce of numbers over mutual-TLS peers: a primitive, with no model, MLX or scheduler behind it.",
	"The runtime's `ring-smoke` is a fixed scalar all-sum, \"not distributed inference\"; worker dispatch hooks are \"not integrated yet\" (runtimes/README.md).",
	"internal/privateruntime and client/private_runtime.go download and verify a signed runtime bundle from the coordinator; they do not shard or run models.",
	"runtimes/bridge runs ONE supervised single-host inference (and a sequential champion/challenger pair); PlanRingSmoke only generates smoke-test arguments.",
}

const (
	statementShardedFits = "Sharing across peers: planned layout fits, but multi-host execution is not available yet; single-host run is available."
	statementNoShard     = "Sharing across peers: multi-host execution is not available yet. Only single-host runs exist today."
)

func buildSharing(anySharded bool, singleRunnable bool, distributed string) SharingReport {
	s := SharingReport{SingleHostExecution: "available", MultiHostExecution: "unavailable", Evidence: append([]string(nil), sharingEvidence...)}
	if !singleRunnable {
		s.SingleHostExecution = "blocked"
	}
	if anySharded {
		s.Statement = statementShardedFits
		if !singleRunnable {
			s.Statement = "Sharing across peers: planned layout fits, but multi-host execution is not available yet; single-host run is blocked until the items below are fixed."
		}
	} else {
		s.Statement = statementNoShard
	}
	if distributed != "" {
		s.Evidence = append(s.Evidence, fmt.Sprintf("The runtime's own probe reports distributed_execution=%q.", distributed))
	}
	return s
}

// howToUse is the short usage text for the recommended model. It names only what
// exists: there is no web UI for inference, and there is no `nexal infer`
// subcommand; the single-model path is the separate nexal-mlx-job supervisor
// built from runtimes/bridge/cmd/nexal-mlx-job (runtimes/QWEN-PHI.md).
func howToUse(e CatalogEntry, hostName string, runnable bool) []string {
	lines := []string{
		"There is no web page or app screen for chatting with a model today. The only way to run one is the command line on the Mac that holds the model.",
	}
	if !runnable {
		lines = append(lines, "This model is not runnable yet. Fix the blockers listed above first.")
	}
	lines = append(lines,
		installLine(e, hostName),
		"2. Check the runtime: python3 -I /ABSOLUTE/APPROVED/nexal_mlx_entry.py probe",
		"3. Run one prompt (the separate nexal-mlx-job tool from runtimes/bridge, not a `nexal` subcommand): nexal-mlx-job --owner-policy /owner/policy.json --admission /owner/fresh-admission.json --prompt-file /owner/prompt.txt --attempt-id ATTEMPT --model-manifest-sha256 REVIEWED_DIGEST --max-tokens 256",
		fmt.Sprintf("Limits: prompts up to %d tokens (16 KiB), answers up to %d tokens, greedy decoding.", RuntimeMaxInputTokens, RuntimeMaxOutputTokens),
	)
	return lines
}

// installLine is step 1 of howToUse: the install command when the model is
// pinned, otherwise the manual provisioning path.
func installLine(e CatalogEntry, hostName string) string {
	if e.Installable {
		return fmt.Sprintf("1. On %s, install the pinned, hash-verified files with: nexal inference install %s. That stops at state installed_unmeasured; its output lists the owner steps (measured memory terms, runtime-config.json, release gates) that remain.", hostName, e.ID)
	}
	return fmt.Sprintf("1. On %s, put a reviewed, licensed copy of %s in a read-only folder with a nexal-model-manifest.json (steps in runtimes/README.md, \"Local provisioning\"). It cannot be installed by nexal yet (see the blockers).", hostName, e.DisplayName)
}
