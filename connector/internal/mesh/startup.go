package mesh

import (
	"context"
	"errors"
	"os/exec"
)

// StartupPlan is persisted by the privileged adapter and rebuilt on every
// daemon restart. Arguments are deliberately separate from executable lookup.
type StartupPlan struct {
	SetupKey      string `json:"setupKey"`
	ManagementURL string `json:"managementUrl"`
}

type PlanStore interface {
	Load(context.Context) (StartupPlan, error)
}
type CommandRunner interface {
	Run(context.Context, string, ...string) error
}

// ExecRunner is the production command boundary. It inherits no shell, so
// setup material cannot be interpreted as shell syntax.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

type StaticPlanStore struct{ Plan StartupPlan }

func (s StaticPlanStore) Load(context.Context) (StartupPlan, error) { return s.Plan, nil }

// Controller is the single startup/reconnect path for a real provider. Both
// entry points reload persisted desired state and use Arguments, so a daemon
// restart cannot silently lose strict PQ configuration.
type Controller struct {
	Plans  PlanStore
	Runner CommandRunner
}

func (c Controller) Start(ctx context.Context) error     { return c.reconcile(ctx) }
func (c Controller) Reconnect(ctx context.Context) error { return c.reconcile(ctx) }
func (c Controller) reconcile(ctx context.Context) error {
	if c.Plans == nil || c.Runner == nil {
		return errors.New("mesh controller is incomplete")
	}
	plan, err := c.Plans.Load(ctx)
	if err != nil {
		return errors.New("cannot load mesh startup configuration")
	}
	args, err := plan.Arguments()
	if err != nil {
		return err
	}
	if err := c.Runner.Run(ctx, "netbird", args...); err != nil {
		return errors.New("secure networking service failed to start")
	}
	return nil
}

func (p StartupPlan) Arguments() ([]string, error) {
	if p.SetupKey == "" || p.ManagementURL == "" {
		return nil, errors.New("mesh startup configuration is incomplete")
	}
	return []string{"up", "--setup-key", p.SetupKey, "--management-url", p.ManagementURL, "--enable-rosenpass"}, nil
}

// RuntimePeerEvidence is the only evidence that may lift strict-PQ gates.
// Process exit success or saved configuration is never sufficient.
type RuntimePeerEvidence struct {
	Connected         bool
	QuantumResistance bool
}

func StrictPQReady(peers []RuntimePeerEvidence) bool {
	if len(peers) == 0 {
		return false
	}
	for _, peer := range peers {
		if !peer.Connected || !peer.QuantumResistance {
			return false
		}
	}
	return true
}
