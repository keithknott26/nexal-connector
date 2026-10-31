package mesh

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

type failingPlans struct{}

func (failingPlans) Load(context.Context) (StartupPlan, error) {
	return StartupPlan{}, errors.New("disk read failed: secret-path")
}

type fixedPlans struct{ plan StartupPlan }

func (f fixedPlans) Load(context.Context) (StartupPlan, error) { return f.plan, nil }

type runnerFunc func(ctx context.Context, name string, args ...string) error

func (f runnerFunc) Run(ctx context.Context, name string, args ...string) error {
	return f(ctx, name, args...)
}

func TestReconcileRejectsIncompleteController(t *testing.T) {
	if err := (Controller{}).Start(context.Background()); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("Start() = %v, want incomplete controller error", err)
	}
	if err := (Controller{Plans: &planFixture{}}).Reconnect(context.Background()); err == nil {
		t.Fatal("Reconnect() without a runner must fail")
	}
}

func TestReconcileHidesPlanLoadDetail(t *testing.T) {
	runner := &runnerFixture{}
	err := (Controller{Plans: failingPlans{}, Runner: runner}).Start(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("Start() = %v, want generic load error", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner must not run when the plan cannot load: %v", runner.calls)
	}
}

func TestReconcileRejectsIncompletePlan(t *testing.T) {
	runner := &runnerFixture{}
	c := Controller{Plans: fixedPlans{StartupPlan{SetupKey: "k"}}, Runner: runner}
	if err := c.Start(context.Background()); err == nil || len(runner.calls) != 0 {
		t.Fatalf("Start() = %v calls=%v, want incomplete-plan error and no run", err, runner.calls)
	}
}

func TestReconcileReportsMissingRuntime(t *testing.T) {
	for _, runErr := range []error{os.ErrNotExist, errors.New("secure networking runtime is not installed")} {
		err := (Controller{Plans: &planFixture{}, Runner: &runnerFixture{err: runErr}}).Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "runtime is not installed") {
			t.Errorf("Start() with %v = %v, want not-installed message", runErr, err)
		}
	}
}

func TestReconcileIncludesScrubbedRuntimeDetail(t *testing.T) {
	var credential string
	runner := runnerFunc(func(_ context.Context, _ string, args ...string) error {
		for i, a := range args {
			if a == "--setup-key-file" {
				credential = args[i+1]
			}
		}
		return &RunError{Err: errors.New("exit status 1"), Output: "cannot read " + credential + "\n daemon is   not running"}
	})
	err := (Controller{Plans: &planFixture{}, Runner: runner}).Start(context.Background())
	if err == nil || credential == "" {
		t.Fatalf("Start() = %v credential=%q", err, credential)
	}
	if strings.Contains(err.Error(), credential) || !strings.Contains(err.Error(), "<credential>") || !strings.Contains(err.Error(), "daemon is not running") {
		t.Fatalf("error = %q, want scrubbed single-line detail", err)
	}
	if _, statErr := os.Stat(credential); !os.IsNotExist(statErr) {
		t.Fatalf("credential file %s must be removed, stat err = %v", credential, statErr)
	}
}

func TestRuntimeDetailTruncatesLongOutput(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := runtimeDetail(&RunError{Err: errors.New("e"), Output: long}, "")
	if !strings.HasPrefix(got, "…") || len([]rune(got)) != 301 {
		t.Fatalf("detail length = %d, want ellipsis + 300", len([]rune(got)))
	}
	if runtimeDetail(errors.New("plain"), "") != "" {
		t.Fatal("non-RunError must yield no detail")
	}
}
