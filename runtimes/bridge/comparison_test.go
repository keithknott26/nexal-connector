package runtimebridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComparisonSequentialFreshAdmissionAndShadowFailure(t *testing.T) {
	for _, failShadow := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "prompt")
		// Resolve macOS /var symlinks as required by the native reader.
		if err := os.WriteFile(path, []byte("Summarize evidence"), 0600); err != nil {
			t.Fatal(err)
		}
		path, _ = filepath.EvalSymlinks(path)
		plan := ComparisonPlan{PromptPath: path, MaxTokens: 16, Champion: Candidate{Owner: OwnerPolicy{ConfigSHA256: "qwen"}}, Challenger: Candidate{Owner: OwnerPolicy{ConfigSHA256: "phi"}}}
		id := func(_ context.Context, c Candidate) (string, string, error) {
			if c.Owner.ConfigSHA256 == "qwen" {
				return "qwen3", strings.Repeat("a", 64), nil
			}
			return "phi3", strings.Repeat("b", 64), nil
		}
		active, acquired, released := false, 0, 0
		acquire := func(_ context.Context, role, model string) (LocalJob, func(), error) {
			if active {
				t.Fatal("overlapping reservations")
			}
			active = true
			acquired++
			return LocalJob{AttemptID: role, ModelManifestSHA256: model}, func() { active = false; released++ }, nil
		}
		runner := func(_ context.Context, _ OwnerPolicy, j LocalJob) (InferenceResult, error) {
			if !active || j.PromptPath != path || j.MaxTokens != 16 {
				t.Fatal("incorrect admission/input")
			}
			if j.AttemptID == "challenger" && failShadow {
				return InferenceResult{}, ErrProcess
			}
			return InferenceResult{AttemptID: j.AttemptID, Text: "advisory"}, nil
		}
		got, err := runComparison(context.Background(), plan, acquire, id, runner)
		if err != nil || active || acquired != 2 || released != 2 || got.AutomaticPromotion || got.Authority != "advisory_only" || got.Champion.Text != "advisory" {
			t.Fatalf("%+v %v", got, err)
		}
		if failShadow && (got.Challenger != nil || got.ChallengerStatus != "unavailable") {
			t.Fatal("shadow failure masked")
		}
		if !failShadow && (got.Challenger == nil || got.ChallengerStatus != "completed") {
			t.Fatal("missing comparison")
		}
	}
}

func TestComparisonRejectsDuplicateModelsBeforeAdmission(t *testing.T) {
	id := func(context.Context, Candidate) (string, string, error) { return "qwen3", strings.Repeat("a", 64), nil }
	acquired := false
	acquire := func(context.Context, string, string) (LocalJob, func(), error) {
		acquired = true
		return LocalJob{}, nil, nil
	}
	_, err := runComparison(context.Background(), ComparisonPlan{MaxTokens: 16}, acquire, id, nil)
	if !errors.Is(err, ErrPolicy) || acquired {
		t.Fatal("duplicate pair admitted", err)
	}
}

func TestComparisonStopsAfterPrimaryFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt")
	_ = os.WriteFile(path, []byte("test"), 0600)
	path, _ = filepath.EvalSymlinks(path)
	n, released := 0, 0
	id := func(context.Context, Candidate) (string, string, error) {
		n++
		if n == 1 {
			return "qwen3", strings.Repeat("a", 64), nil
		}
		return "phi3", strings.Repeat("b", 64), nil
	}
	acquired := 0
	acquire := func(_ context.Context, _ string, m string) (LocalJob, func(), error) {
		acquired++
		return LocalJob{AttemptID: "a", ModelManifestSHA256: m}, func() { released++ }, nil
	}
	runner := func(context.Context, OwnerPolicy, LocalJob) (InferenceResult, error) {
		return InferenceResult{}, ErrProcess
	}
	_, err := runComparison(context.Background(), ComparisonPlan{PromptPath: path, MaxTokens: 16}, acquire, id, runner)
	if !errors.Is(err, ErrProcess) || acquired != 1 || released != 1 {
		t.Fatal(err, acquired, released)
	}
}

func TestComparisonRejectsChangedEvidenceBeforeShadowRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt")
	_ = os.WriteFile(path, []byte("original"), 0600)
	path, _ = filepath.EvalSymlinks(path)
	n, released, runs := 0, 0, 0
	id := func(context.Context, Candidate) (string, string, error) {
		n++
		if n == 1 {
			return "qwen3", strings.Repeat("a", 64), nil
		}
		return "phi3", strings.Repeat("b", 64), nil
	}
	acquire := func(_ context.Context, role, m string) (LocalJob, func(), error) {
		if role == "challenger" {
			_ = os.WriteFile(path, []byte("changed"), 0600)
		}
		return LocalJob{AttemptID: role, ModelManifestSHA256: m}, func() { released++ }, nil
	}
	runner := func(context.Context, OwnerPolicy, LocalJob) (InferenceResult, error) {
		runs++
		return InferenceResult{Text: "primary"}, nil
	}
	got, err := runComparison(context.Background(), ComparisonPlan{PromptPath: path, MaxTokens: 16}, acquire, id, runner)
	if err != nil || runs != 1 || released != 2 || got.ChallengerStatus != "unavailable" {
		t.Fatal(got, err, runs, released)
	}
}

func TestComparisonRejectsReusedAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompt")
	_ = os.WriteFile(path, []byte("original"), 0600)
	path, _ = filepath.EvalSymlinks(path)
	n, released, runs := 0, 0, 0
	id := func(context.Context, Candidate) (string, string, error) {
		n++
		if n == 1 {
			return "qwen3", strings.Repeat("a", 64), nil
		}
		return "phi3", strings.Repeat("b", 64), nil
	}
	acquire := func(_ context.Context, _ string, m string) (LocalJob, func(), error) {
		return LocalJob{AttemptID: "reused", ModelManifestSHA256: m}, func() { released++ }, nil
	}
	runner := func(context.Context, OwnerPolicy, LocalJob) (InferenceResult, error) {
		runs++
		return InferenceResult{}, nil
	}
	got, err := runComparison(context.Background(), ComparisonPlan{PromptPath: path, MaxTokens: 16}, acquire, id, runner)
	if err != nil || runs != 1 || released != 2 || got.ChallengerStatus != "unavailable" {
		t.Fatal(got, err, runs, released)
	}
}
