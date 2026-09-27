package mesh

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

type planFixture struct{ loads int }

func (p *planFixture) Load(context.Context) (StartupPlan, error) {
	p.loads++
	return StartupPlan{SetupKey: "secret", ManagementURL: "https://management.example"}, nil
}

type runnerFixture struct {
	calls [][]string
	err   error
}

func (r *runnerFixture) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.err
}

func TestStartupFailureDoesNotLeakSetupKey(t *testing.T) {
	runner := &runnerFixture{err: errors.New("backend failed with secret")}
	err := (Controller{Plans: &planFixture{}, Runner: runner}).Start(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(strings.ToLower(err.Error()), "netbird") {
		t.Fatalf("unsafe public error: %v", err)
	}
}

func TestStartupAlwaysRequiresRosenpassWithoutPermissiveMode(t *testing.T) {
	args, err := (StartupPlan{SetupKey: "secret", ManagementURL: "https://management.example"}).Arguments("/private/tmp/credential")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "--enable-rosenpass") {
		t.Fatalf("missing required flag: %v", args)
	}
	if strings.Contains(strings.Join(args, " "), "rosenpass-permissive") {
		t.Fatalf("permissive mode is forbidden: %v", args)
	}
}

func TestStartupNeverPlacesCredentialInArguments(t *testing.T) {
	runner := &runnerFixture{}
	if err := (Controller{Plans: &planFixture{}, Runner: runner}).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(runner.calls[0], " ")
	if strings.Contains(joined, "secret") || !strings.Contains(joined, "--setup-key-file") || strings.Contains(joined, "--setup-key ") {
		t.Fatalf("credential was not isolated from process arguments: %s", joined)
	}
}

func TestStartAndReconnectReapplyStrictPlan(t *testing.T) {
	plans, runner := &planFixture{}, &runnerFixture{}
	c := Controller{Plans: plans, Runner: runner}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Reconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if plans.loads != 2 || len(runner.calls) != 2 {
		t.Fatalf("plan was not reapplied: loads=%d calls=%d", plans.loads, len(runner.calls))
	}
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if !strings.Contains(joined, "--enable-rosenpass") || strings.Contains(joined, "--rosenpass-permissive") {
			t.Fatalf("unsafe startup: %s", joined)
		}
	}
}

func TestStrictPQNeedsEveryConnectedPeerToReportRuntimeEvidence(t *testing.T) {
	if StrictPQReady(nil) {
		t.Fatal("empty status cannot prove PQ")
	}
	if StrictPQReady([]RuntimePeerEvidence{{Connected: true}}) {
		t.Fatal("command success cannot prove PQ")
	}
	if StrictPQReady([]RuntimePeerEvidence{{Connected: true, QuantumResistance: true}, {Connected: false, QuantumResistance: true}}) {
		t.Fatal("disconnected peer accepted")
	}
	if StrictPQReady([]RuntimePeerEvidence{{Connected: true, QuantumResistance: true}}) {
		t.Fatal("configuration flags accepted without key-installation evidence")
	}
}
