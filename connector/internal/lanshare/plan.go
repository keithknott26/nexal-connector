package lanshare

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Step is one change to the system.
//
// Argv is the exact command, already split, never a shell string. Reason is
// shown to the owner before anything runs, because a tool that asks for an
// administrator password owes an explanation of what the password will be used
// for -- and "trust me" is not one.
type Step struct {
	Name      string   `json:"name"`
	Reason    string   `json:"reason"`
	Argv      []string `json:"argv"`
	NeedsRoot bool     `json:"needsRoot"`
}

// String renders the step as a copyable command. Used in output so an owner who
// declines elevation can run it deliberately instead.
func (s Step) String() string {
	parts := make([]string, 0, len(s.Argv))
	for _, a := range s.Argv {
		if strings.ContainsAny(a, " \t\"'\\$`") {
			parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
			continue
		}
		parts = append(parts, a)
	}
	command := strings.Join(parts, " ")
	if s.NeedsRoot {
		return "sudo " + command
	}
	return command
}

// Plan is the ordered set of steps that would make State match Desired.
//
// An empty Plan means the system already matches. Blocked is non-empty when no
// plan can succeed, which is different from an empty plan and must never be
// reported as "already configured".
type Plan struct {
	Steps   []Step   `json:"steps"`
	Blocked []string `json:"blocked,omitempty"`
}

// NeedsRoot reports whether any step requires elevation, so a caller can decide
// whether to prompt at all.
func (p Plan) NeedsRoot() bool {
	for _, s := range p.Steps {
		if s.NeedsRoot {
			return true
		}
	}
	return false
}

// BuildPlan computes the minimal set of changes.
//
// "Minimal" is the whole point of the design: each condition is checked
// independently and a step is emitted only for a condition that is actually
// unmet. Re-running after a partial success asks for strictly less, and a
// system that already matches asks for nothing -- including no password.
func BuildPlan(d Desired, s State) (Plan, error) {
	if err := d.Validate(); err != nil {
		return Plan{}, err
	}
	var p Plan

	// Blocking conditions first. These cannot be fixed by a command, so emitting
	// steps alongside them would invite a caller to run half a plan that cannot
	// work.
	if s.Filesystem != "" && !s.APFS() {
		p.Blocked = append(p.Blocked, fmt.Sprintf(
			"%s is on a %s volume, but macOS requires APFS for a shared Time Machine destination; choose a folder on an APFS volume",
			d.Path, s.Filesystem))
	}
	for fact, reason := range s.Unknown {
		p.Blocked = append(p.Blocked, fmt.Sprintf("could not determine %s: %s", fact, reason))
	}
	if len(p.Blocked) > 0 {
		return p, nil
	}

	// An existing share with the same name pointing somewhere else is a conflict,
	// not something to silently repoint: another person's backups may be going
	// there right now.
	if s.ShareExists && s.SharePath != "" && s.SharePath != d.Path {
		p.Blocked = append(p.Blocked, fmt.Sprintf(
			"a share named %q already publishes %s, not %s; remove it or choose a different name rather than repointing a share that may be in use",
			d.Name, s.SharePath, d.Path))
		return p, nil
	}

	if !s.FileSharingEnabled {
		p.Steps = append(p.Steps, Step{
			Name:      "enable-file-sharing",
			Reason:    "macOS's SMB server is not running, so no Mac on the network can reach a share on this one",
			Argv:      []string{LaunchctlPath, "enable", smbdService},
			NeedsRoot: true,
		})
	}
	if !s.ShareExists {
		// -s 001 is SMB only: AFP is removed in macOS 27 and enabling a protocol
		// nothing will use is extra exposure for no benefit.
		// -g 000 refuses guest access on the share.
		p.Steps = append(p.Steps, Step{
			Name:      "create-share",
			Reason:    fmt.Sprintf("publish %s over SMB as %q, with guest access refused", d.Path, d.Name),
			Argv:      []string{SharingPath, "-a", d.Path, "-S", d.Name, "-n", d.Name, "-s", "001", "-g", "000"},
			NeedsRoot: true,
		})
	}
	if !s.TimeMachineEnabled {
		p.Steps = append(p.Steps, Step{
			Name:      "mark-time-machine-destination",
			Reason:    "without this flag Time Machine on another Mac will not offer the share as a backup destination",
			Argv:      []string{SharingPath, "-e", d.Name, "-t", "1"},
			NeedsRoot: true,
		})
	}
	return p, nil
}

// Elevator runs a command with administrator rights.
type Elevator interface {
	Elevate(ctx context.Context, argv []string) (string, error)
}

// Apply executes a plan.
//
// Steps that do not need root run through the plain runner. Steps that do run
// directly when this process is already root, and through the Elevator when it
// is not. Execution stops at the first failure: the steps are ordered
// dependencies (a share must exist before it can be flagged), so continuing past
// a failure would produce misleading errors about the steps that follow.
func Apply(ctx context.Context, p Plan, runner Runner, elevator Elevator) ([]string, error) {
	if len(p.Blocked) > 0 {
		return nil, fmt.Errorf("lanshare: cannot proceed: %s", strings.Join(p.Blocked, "; "))
	}
	done := make([]string, 0, len(p.Steps))
	root := os.Geteuid() == 0
	for _, step := range p.Steps {
		var err error
		switch {
		case !step.NeedsRoot || root:
			_, err = runner.Run(ctx, step.Argv[0], step.Argv[1:]...)
		case elevator == nil:
			return done, fmt.Errorf("lanshare: %s needs administrator rights and none were offered; run it yourself with:\n  %s", step.Name, step)
		default:
			_, err = elevator.Elevate(ctx, step.Argv)
		}
		if err != nil {
			return done, fmt.Errorf("lanshare: %s failed (%w); the equivalent command is:\n  %s", step.Name, err, step)
		}
		done = append(done, step.Name)
	}
	return done, nil
}

// osascriptElevator prompts with macOS's own authorization dialog.
//
// This is Apple's supported mechanism for occasional privileged work from an
// app that has no installed privileged helper: the system draws the dialog, the
// password is entered into it and never passes through this process, and
// nothing is stored. A SMJobBless helper would be the right answer for frequent
// or unattended elevation; for "configure a share once" it is a large permanent
// attack surface to solve a rare problem.
type osascriptElevator struct {
	runner Runner
}

// NewOsascriptElevator returns an Elevator backed by the system authorization
// dialog.
func NewOsascriptElevator(runner Runner) Elevator { return &osascriptElevator{runner: runner} }

// shellQuoteStrict renders one argv element for AppleScript's `do shell script`.
//
// THIS IS THE ONE PLACE A STRING BECOMES SHELL, so it refuses rather than
// escapes anything outside a conservative allowlist. Every argument this package
// elevates is either a constant, a validated share name, or a validated absolute
// path, so a rejection here means a bug upstream let something through -- and
// failing loudly beats quoting cleverly.
func shellQuoteStrict(arg string) (string, error) {
	if arg == "" {
		return "''", nil
	}
	for _, r := range arg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == '/', r == ' ':
		default:
			return "", fmt.Errorf("lanshare: refusing to elevate a command containing %q", r)
		}
	}
	// Even within the allowlist the value is single-quoted, so a space cannot
	// split one argument into two.
	return "'" + arg + "'", nil
}

func (e *osascriptElevator) Elevate(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("lanshare: nothing to elevate")
	}
	if !strings.HasPrefix(argv[0], "/") {
		// A relative program name resolved through PATH by the shell is how an
		// elevated command ends up being a different program than intended.
		return "", errors.New("lanshare: refusing to elevate a command that is not an absolute path")
	}
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		q, err := shellQuoteStrict(a)
		if err != nil {
			return "", err
		}
		quoted = append(quoted, q)
	}
	command := strings.Join(quoted, " ")
	// The AppleScript string itself must not be breakable. After
	// shellQuoteStrict there is no double quote or backslash left to escape, but
	// the check is repeated rather than assumed, because the cost of being wrong
	// here is arbitrary code running as root.
	if strings.ContainsAny(command, `"\`+"\n\r") {
		return "", errors.New("lanshare: refusing to elevate a command that cannot be safely quoted")
	}
	script := fmt.Sprintf(`do shell script "%s" with administrator privileges`, command)
	return e.runner.Run(ctx, "/usr/bin/osascript", "-e", script)
}
