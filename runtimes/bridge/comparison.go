package runtimebridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"
)

// Candidate is a reviewed local installation. Model identity is derived from its
// pinned config/manifest, never from an untrusted job's requested architecture.
type Candidate struct{ Owner OwnerPolicy }

type ComparisonPlan struct {
	Champion   Candidate
	Challenger Candidate
	PromptPath string
	MaxTokens  int
}

// AcquireComparisonJob must reserve through the caller's existing admission
// authority and create a FRESH model-bound snapshot immediately before each run.
// It returns a release callback, called even on inference failure. It must clean
// up its own partial reservation on error. No second scheduler is created here.
type AcquireComparisonJob func(ctx context.Context, role, modelDigest string) (LocalJob, func(), error)

type ComparisonResult struct {
	SchemaVersion      int              `json:"schema_version"`
	PromptSHA256       string           `json:"prompt_sha256"`
	Champion           InferenceResult  `json:"champion"`
	Challenger         *InferenceResult `json:"challenger,omitempty"`
	ChallengerStatus   string           `json:"challenger_status"`
	AutomaticPromotion bool             `json:"automatic_promotion"`
	Authority          string           `json:"authority"`
}

// RunChampionChallenger runs the primary first, then a shadow candidate in a
// separate supervised process after releasing the first reservation. It makes
// no accuracy claim and never promotes a model or executes its suggestions.
func RunChampionChallenger(ctx context.Context, plan ComparisonPlan, acquire AcquireComparisonJob) (ComparisonResult, error) {
	return runComparison(ctx, plan, acquire, comparisonIdentity, RunLocalInference)
}

func comparisonIdentity(ctx context.Context, candidate Candidate) (string, string, error) {
	config, err := verifyPolicy(ctx, candidate.Owner)
	if err != nil {
		return "", "", err
	}
	raw, err := readLocalFile(config.ModelDirectory+"/nexal-model-manifest.json", maxTreeBytes, false, false)
	if err != nil || !matchesDigest(raw, config.ModelManifestSHA256) {
		return "", "", ErrPolicy
	}
	var manifest struct {
		ModelType string `json:"model_type"`
	}
	// Full strict model validation still runs in the Python adapter before load.
	if json.Unmarshal(raw, &manifest) != nil {
		return "", "", ErrPolicy
	}
	return manifest.ModelType, config.ModelManifestSHA256, nil
}

type identityReader func(context.Context, Candidate) (string, string, error)
type inferenceRunner func(context.Context, OwnerPolicy, LocalJob) (InferenceResult, error)

func runComparison(ctx context.Context, plan ComparisonPlan, acquire AcquireComparisonJob, identity identityReader, run inferenceRunner) (ComparisonResult, error) {
	var result ComparisonResult
	if ctx == nil || acquire == nil || plan.MaxTokens < 1 || plan.MaxTokens > 512 {
		return result, ErrInput
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	a, ad, err := identity(ctx, plan.Champion)
	if err != nil {
		return result, err
	}
	b, bd, err := identity(ctx, plan.Challenger)
	if err != nil {
		return result, err
	}
	if !((a == "qwen3" && b == "phi3") || (a == "phi3" && b == "qwen3")) || !validDigest(ad) || !validDigest(bd) || ad == bd {
		return result, ErrPolicy
	}
	prompt, err := readLocalFile(plan.PromptPath, 16*1024, false, false)
	if err != nil || !utf8.Valid(prompt) || len(bytes.TrimSpace(prompt)) == 0 {
		return result, ErrInput
	}
	sum := sha256.Sum256(prompt)
	result = ComparisonResult{SchemaVersion: 1, PromptSHA256: hex.EncodeToString(sum[:]), ChallengerStatus: "not_run", Authority: "advisory_only"}
	attempt := ""
	runOne := func(role, model string, owner OwnerPolicy) (InferenceResult, error) {
		var empty InferenceResult
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		job, release, err := acquire(ctx, role, model)
		if err != nil {
			return empty, err
		}
		if release == nil {
			return empty, ErrAdmission
		}
		defer release()
		if job.ModelManifestSHA256 != model || job.AttemptID == "" || (attempt != "" && attempt == job.AttemptID) {
			return empty, ErrAdmission
		}
		attempt = job.AttemptID
		job.PromptPath, job.MaxTokens = plan.PromptPath, plan.MaxTokens
		current, err := readLocalFile(plan.PromptPath, 16*1024, false, false)
		if err != nil || !bytes.Equal(current, prompt) {
			return empty, ErrInput
		}
		output, err := run(ctx, owner, job)
		if err != nil {
			return empty, err
		}
		current, err = readLocalFile(plan.PromptPath, 16*1024, false, false)
		if err != nil || !bytes.Equal(current, prompt) {
			return empty, ErrInput
		}
		return output, nil
	}
	result.Champion, err = runOne("champion", ad, plan.Champion.Owner)
	if err != nil {
		return result, err
	}
	challenger, err := runOne("challenger", bd, plan.Challenger.Owner)
	if err != nil {
		result.ChallengerStatus = "unavailable"
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		// Failure of the shadow run does not discard the primary's valid result.
		return result, nil
	}
	result.Challenger = &challenger
	result.ChallengerStatus = "completed"
	return result, nil
}
