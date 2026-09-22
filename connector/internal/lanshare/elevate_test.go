package lanshare

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// Everything in this file guards a command that is about to run as root.

func TestElevationRefusesEveryShellMetacharacter(t *testing.T) {
	runner := &fakeRunner{out: map[string]string{}}
	e := NewOsascriptElevator(runner)
	// Each of these, if it reached `do shell script`, would run something other
	// than the intended command with administrator rights.
	for _, hostile := range []string{
		"a;rm -rf /", "a&&id", "a||id", "a|id", "a`id`", "a$(id)", "a$HOME",
		`a"b`, `a\b`, "a\nid", "a\rid", "a>file", "a<file", "a*", "a?", "a[b]",
		"a{b}", "a#c", "a!b", "a~b", "a'b",
	} {
		if _, err := e.Elevate(context.Background(), []string{SharingPath, "-S", hostile}); err == nil {
			t.Errorf("elevated a command containing %q", hostile)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("a hostile argument reached the runner: %v", runner.calls)
	}
}

func TestElevationRefusesARelativeProgramPath(t *testing.T) {
	runner := &fakeRunner{out: map[string]string{}}
	e := NewOsascriptElevator(runner)
	if _, err := e.Elevate(context.Background(), []string{"sharing", "-l"}); err == nil {
		t.Fatal("elevated a program resolved through PATH")
	}
	if _, err := e.Elevate(context.Background(), nil); err == nil {
		t.Fatal("elevated an empty command")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("a refused command still reached the runner: %v", runner.calls)
	}
}

func TestElevationBuildsASingleQuotedAdministratorScript(t *testing.T) {
	runner := &fakeRunner{out: map[string]string{"/usr/bin/osascript": ""}}
	e := NewOsascriptElevator(runner)
	if _, err := e.Elevate(context.Background(), []string{SharingPath, "-a", "/Users/me/Backups", "-S", "Backups"}); err != nil {
		t.Fatalf("Elevate: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected one call, got %v", runner.calls)
	}
	call := runner.calls[0]
	if call[0] != "/usr/bin/osascript" || call[1] != "-e" {
		t.Fatalf("unexpected invocation: %v", call)
	}
	script := call[2]
	if !strings.HasSuffix(script, "with administrator privileges") {
		t.Errorf("the script does not request administrator rights: %s", script)
	}
	for _, want := range []string{"'" + SharingPath + "'", "'-a'", "'/Users/me/Backups'", "'Backups'"} {
		if !strings.Contains(script, want) {
			t.Errorf("%s missing from script: %s", want, script)
		}
	}
	// A space inside a quoted argument must not split it into two arguments.
	if strings.Contains(script, "/Users/me/Backups'") && strings.Count(script, "'") != 10 {
		t.Errorf("unbalanced quoting: %s", script)
	}
}

// Apply must not prompt for steps that do not need root.
func TestApplyRunsUnprivilegedStepsWithoutElevating(t *testing.T) {
	runner := &fakeRunner{out: map[string]string{"/bin/echo": ""}}
	elevator := &countingElevator{}
	plan := Plan{Steps: []Step{{Name: "harmless", Argv: []string{"/bin/echo", "hello"}}}}
	done, err := Apply(context.Background(), plan, runner, elevator)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(done) != 1 || elevator.calls != 0 {
		t.Fatalf("done=%v elevations=%d", done, elevator.calls)
	}
}

func TestApplyStopsAtTheFirstFailureRatherThanContinuing(t *testing.T) {
	runner := &fakeRunner{
		out:  map[string]string{"/bin/echo": ""},
		fail: map[string]error{"/bin/false": errors.New("exit status 1")},
	}
	plan := Plan{Steps: []Step{
		{Name: "first", Argv: []string{"/bin/echo", "a"}},
		{Name: "second", Argv: []string{"/bin/false"}},
		{Name: "third", Argv: []string{"/bin/echo", "c"}},
	}}
	done, err := Apply(context.Background(), plan, runner, nil)
	if err == nil {
		t.Fatal("Apply reported success despite a failing step")
	}
	if len(done) != 1 || done[0] != "first" {
		t.Fatalf("completed steps were %v", done)
	}
	if !strings.Contains(err.Error(), "the equivalent command is") {
		t.Errorf("the failure does not show a runnable command: %v", err)
	}
}

// Declining elevation must leave the owner able to do it themselves.
func TestApplyWithoutAnElevatorReportsTheExactCommand(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; the unprivileged path cannot be exercised")
	}
	runner := &fakeRunner{out: map[string]string{}}
	plan := Plan{Steps: []Step{{Name: "create-share", Argv: []string{SharingPath, "-a", "/x"}, NeedsRoot: true}}}
	_, err := Apply(context.Background(), plan, runner, nil)
	if err == nil {
		t.Fatal("a root step ran without root or an elevator")
	}
	if !strings.Contains(err.Error(), "sudo "+SharingPath) {
		t.Errorf("the error does not include the command to run: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("a privileged step reached the plain runner: %v", runner.calls)
	}
}

func TestApplyRefusesABlockedPlan(t *testing.T) {
	runner := &fakeRunner{out: map[string]string{}}
	_, err := Apply(context.Background(), Plan{Blocked: []string{"not APFS"}}, runner, nil)
	if err == nil {
		t.Fatal("a blocked plan was applied")
	}
	if len(runner.calls) != 0 {
		t.Fatal("a blocked plan still ran a command")
	}
}

type countingElevator struct{ calls int }

func (c *countingElevator) Elevate(context.Context, []string) (string, error) {
	c.calls++
	return "", nil
}

// The destination URL must never carry a password.
func TestDestinationURLOmitsThePassword(t *testing.T) {
	url, err := DestinationURL("mac-mini.local", "kknott", "Backups")
	if err != nil {
		t.Fatalf("DestinationURL: %v", err)
	}
	if url != "smb://kknott@mac-mini.local/Backups" {
		t.Fatalf("got %s", url)
	}
	if strings.Contains(url, ":") != strings.Contains("smb://", ":") {
		t.Errorf("unexpected colon, which would indicate a password: %s", url)
	}
	for _, hostile := range []string{"host;id", "host/../x", "host name", "host`id`"} {
		if _, err := DestinationURL(hostile, "u", "Backups"); err == nil {
			t.Errorf("accepted hostile host %q", hostile)
		}
	}
	if _, err := DestinationURL("host", "user", "bad name"); err == nil {
		t.Error("accepted a hostile share name")
	}
}
