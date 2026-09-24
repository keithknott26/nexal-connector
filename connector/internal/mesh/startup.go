package mesh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
	path, err := trustedExecutable(name)
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return &RunError{Err: err, Output: string(out)}
	}
	return nil
}

// RunError carries the runtime's own output so a failure can say WHY it failed
// ("daemon is not running", "invalid setup key") instead of a fixed guess.
type RunError struct {
	Err    error
	Output string
}

func (e *RunError) Error() string { return e.Err.Error() }
func (e *RunError) Unwrap() error { return e.Err }

// runtimeDetail returns a short, single-line tail of the runtime's output with
// the credential file path removed. The setup key itself is never on the
// command line or in this output path; the file path is scrubbed anyway so no
// temp-file name leaks into UI text or logs.
func runtimeDetail(err error, credentialPath string) string {
	var re *RunError
	if !errors.As(err, &re) {
		return ""
	}
	text := strings.Join(strings.Fields(re.Output), " ")
	if credentialPath != "" {
		text = strings.ReplaceAll(text, credentialPath, "<credential>")
	}
	const limit = 300
	if len(text) > limit {
		text = "…" + text[len(text)-limit:]
	}
	return text
}

// trustedExecutable deliberately does not search the inherited PATH. The menu
// app strips ambient developer paths, and accepting whichever binary appears
// first would let an unrelated package replace the secure-networking boundary.
// Release packaging may place the runtime beside the neXal helper; the official
// macOS installer uses /usr/local/bin, while Homebrew on Apple Silicon uses
// /opt/homebrew/bin.
func trustedExecutable(name string) (string, error) {
	if name != "nexal-network" {
		return "", errors.New("unsupported secure networking runtime")
	}
	candidates := []string{}
	if current, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(current), name))
	}
	// Development fallbacks support an independently installed upstream runtime.
	// Release builds always resolve the sibling neXal-branded executable first.
	candidates = append(candidates, "/usr/local/bin/netbird", "/opt/homebrew/bin/netbird")
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("secure networking runtime is not installed")
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
	credential, err := os.CreateTemp("", "nexal-mesh-credential-*")
	if err != nil {
		return errors.New("cannot prepare secure networking credential")
	}
	credentialPath := credential.Name()
	defer os.Remove(credentialPath)
	if err := credential.Chmod(0o600); err != nil {
		credential.Close()
		return errors.New("cannot protect secure networking credential")
	}
	if _, err := credential.WriteString(plan.SetupKey); err != nil {
		credential.Close()
		return errors.New("cannot prepare secure networking credential")
	}
	if err := credential.Close(); err != nil {
		return errors.New("cannot prepare secure networking credential")
	}
	args, err := plan.Arguments(credentialPath)
	if err != nil {
		return err
	}
	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.Runner.Run(startupCtx, "nexal-network", args...); err != nil {
		if ctx.Err() == nil && errors.Is(startupCtx.Err(), context.DeadlineExceeded) {
			return errors.New("secure networking service did not finish authentication; restart the neXal networking service and retry")
		}
		if errors.Is(err, os.ErrNotExist) || err.Error() == "secure networking runtime is not installed" {
			return errors.New("secure networking runtime is not installed; reinstall neXal Connector 0.2.8 or later")
		}
		msg := "secure networking service rejected startup; verify its macOS system service is installed and running"
		if detail := runtimeDetail(err, credentialPath); detail != "" {
			msg += " (" + detail + ")"
		}
		return errors.New(msg)
	}
	return nil
}

func (p StartupPlan) Arguments(credentialPath string) ([]string, error) {
	if p.SetupKey == "" || p.ManagementURL == "" || credentialPath == "" {
		return nil, errors.New("mesh startup configuration is incomplete")
	}
	return []string{"up", "--setup-key-file", credentialPath, "--management-url", p.ManagementURL, "--enable-rosenpass"}, nil
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
