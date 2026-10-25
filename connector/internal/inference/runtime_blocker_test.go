package inference

import "testing"

func TestRuntimeBlockerMapsEveryState(t *testing.T) {
	if got := runtimeBlocker(RuntimeReport{State: RuntimeHealthy}); got != nil {
		t.Fatalf("healthy runtime must not produce a blocker, got %+v", got)
	}
	for _, state := range []string{
		RuntimeNotConfigured, RuntimeIntegrityMismatch, RuntimeUnsupportedPlatform,
		RuntimeDependenciesMissing, RuntimeVersionMismatch, RuntimeProbeFailed, "something_new",
	} {
		b := runtimeBlocker(RuntimeReport{State: state, Detail: "d-" + state})
		if b == nil {
			t.Fatalf("%s: no blocker", state)
		}
		if b.Severity != SeverityBlocker || b.Scope != "this-mac" {
			t.Errorf("%s: severity/scope = %q/%q", state, b.Severity, b.Scope)
		}
		if b.Title == "" || b.Fix == "" {
			t.Errorf("%s: title/fix must be non-empty: %+v", state, b)
		}
		if b.Detail != "d-"+state {
			t.Errorf("%s: detail not carried through: %q", state, b.Detail)
		}
	}
	if got := runtimeBlocker(RuntimeReport{State: RuntimeVersionMismatch}).Code; got != "runtime-version-mismatch" {
		t.Errorf("code = %q, want underscores turned into hyphens", got)
	}
}
