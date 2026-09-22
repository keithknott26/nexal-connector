package lanshare

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner returns canned output per program name and records every call.
type fakeRunner struct {
	out   map[string]string
	fail  map[string]error
	calls [][]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if err, ok := f.fail[name]; ok {
		return "", err
	}
	return f.out[name], nil
}

func shareDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Backups")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	return dir
}

func TestShareNameRefusesAnythingNotPlainlyQuotable(t *testing.T) {
	for _, bad := range []string{
		"", "-flag", "has space", "semi;colon", "quote'", `double"`, "dollar$", "back`tick",
		"pipe|", "amp&", "newline\n", "slash/", "nul\x00", "..", ".",
		strings.Repeat("a", MaxShareNameLength+1),
	} {
		if err := ValidateShareName(bad); err == nil {
			t.Errorf("accepted share name %q", bad)
		}
	}
	for _, good := range []string{"Backups", "time-machine", "mac_mini.backups", "TM2026"} {
		if err := ValidateShareName(good); err != nil {
			t.Errorf("rejected valid share name %q: %v", good, err)
		}
	}
}

// Sharing a symlink would publish whatever it points at now and whatever it is
// repointed at later.
func TestSymlinkedSharePathIsRefused(t *testing.T) {
	dir := shareDir(t)
	link := filepath.Join(filepath.Dir(dir), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err := ValidateSharePath(link)
	if err == nil {
		t.Fatal("a symlinked share path was accepted")
	}
	if !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("the refusal does not explain the symlink: %v", err)
	}
}

func TestSystemLocationsAndRootAreRefused(t *testing.T) {
	for _, bad := range []string{"/", "/System", "/usr/local", "/etc", "/var/tmp", "/Library/Caches", "/Applications"} {
		if err := ValidateSharePath(bad); err == nil {
			t.Errorf("accepted %q as a share path", bad)
		}
	}
	if err := ValidateSharePath("relative/path"); err == nil {
		t.Error("accepted a relative share path")
	}
	if err := ValidateSharePath("/tmp/../tmp"); err == nil {
		t.Error("accepted a non-canonical share path")
	}
}

func TestMissingDirectoryIsRefusedWithAUsefulMessage(t *testing.T) {
	err := ValidateSharePath(filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		t.Fatal("a nonexistent path was accepted")
	}
	if !strings.Contains(err.Error(), "create the folder first") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}

// The core of "elevate only when needed": a system that already matches must
// produce no steps at all, so nothing prompts.
func TestAnAlreadyConfiguredSystemPlansNothing(t *testing.T) {
	dir := shareDir(t)
	d := Desired{Path: dir, Name: "Backups"}
	state := State{
		FileSharingEnabled: true, ShareExists: true, SharePath: dir,
		TimeMachineEnabled: true, Filesystem: "apfs",
	}
	if !state.Satisfied(d) {
		t.Fatal("Satisfied said no for a fully configured state")
	}
	plan, err := BuildPlan(d, state)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Steps) != 0 || len(plan.Blocked) != 0 {
		t.Fatalf("a configured system produced %d steps and %d blockers", len(plan.Steps), len(plan.Blocked))
	}
	if plan.NeedsRoot() {
		t.Error("a configured system would still ask for a password")
	}
}

// Each unmet condition contributes exactly one step, so re-running after a
// partial success asks for strictly less.
func TestOnlyUnmetConditionsProduceSteps(t *testing.T) {
	dir := shareDir(t)
	d := Desired{Path: dir, Name: "Backups"}
	full, err := BuildPlan(d, State{Filesystem: "apfs"})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(full.Steps) != 3 {
		t.Fatalf("an unconfigured system produced %d steps, want 3", len(full.Steps))
	}
	partial, err := BuildPlan(d, State{Filesystem: "apfs", FileSharingEnabled: true, ShareExists: true, SharePath: dir})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(partial.Steps) != 1 || partial.Steps[0].Name != "mark-time-machine-destination" {
		t.Fatalf("a partially configured system produced %v", stepNames(partial))
	}
}

func stepNames(p Plan) []string {
	out := make([]string, 0, len(p.Steps))
	for _, s := range p.Steps {
		out = append(out, s.Name)
	}
	return out
}

// Apple requires APFS. A non-APFS volume cannot be fixed by a command, so it
// must block rather than emit steps that would fail.
func TestNonAPFSVolumeBlocksWithoutEmittingSteps(t *testing.T) {
	dir := shareDir(t)
	plan, err := BuildPlan(Desired{Path: dir, Name: "Backups"}, State{Filesystem: "hfs+"})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Fatalf("a blocked plan still contained %v", stepNames(plan))
	}
	if len(plan.Blocked) == 0 || !strings.Contains(plan.Blocked[0], "APFS") {
		t.Fatalf("blocked reasons were %v", plan.Blocked)
	}
}

// An unknown fact must block, never default to "not configured" -- otherwise a
// failed `sharing -l` would ask for a password to create a share that exists.
func TestAnUndeterminedFactBlocksRatherThanAssuming(t *testing.T) {
	dir := shareDir(t)
	plan, err := BuildPlan(Desired{Path: dir, Name: "Backups"},
		State{Filesystem: "apfs", Unknown: map[string]string{"whether the share already exists": "could not list share points"}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Fatalf("an uncertain state still produced %v", stepNames(plan))
	}
	if len(plan.Blocked) == 0 {
		t.Fatal("an uncertain state did not block")
	}
}

// Someone else's backups may be going to that share right now.
func TestAnExistingShareWithTheSameNameElsewhereIsNotSilentlyRepointed(t *testing.T) {
	dir := shareDir(t)
	plan, err := BuildPlan(Desired{Path: dir, Name: "Backups"},
		State{Filesystem: "apfs", FileSharingEnabled: true, ShareExists: true, SharePath: "/Volumes/Other/Backups"})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Steps) != 0 {
		t.Fatalf("a conflicting share still produced %v", stepNames(plan))
	}
	if len(plan.Blocked) == 0 || !strings.Contains(plan.Blocked[0], "already publishes") {
		t.Fatalf("blocked reasons were %v", plan.Blocked)
	}
}

func TestPlannedCommandsAreSMBOnlyAndRefuseGuests(t *testing.T) {
	dir := shareDir(t)
	plan, err := BuildPlan(Desired{Path: dir, Name: "Backups"}, State{Filesystem: "apfs"})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var create Step
	for _, s := range plan.Steps {
		if s.Name == "create-share" {
			create = s
		}
		if len(s.Argv) == 0 || !strings.HasPrefix(s.Argv[0], "/") {
			t.Errorf("step %s does not use an absolute program path: %v", s.Name, s.Argv)
		}
		if !s.NeedsRoot {
			t.Errorf("step %s claims it does not need root", s.Name)
		}
		if s.Reason == "" {
			t.Errorf("step %s asks for a password without a reason", s.Name)
		}
	}
	joined := strings.Join(create.Argv, " ")
	if !strings.Contains(joined, "-s 001") {
		t.Errorf("the share is not SMB-only: %v", create.Argv)
	}
	if !strings.Contains(joined, "-g 000") {
		t.Errorf("guest access is not refused: %v", create.Argv)
	}
}

func TestStepStringIsACopyableSudoCommand(t *testing.T) {
	s := Step{Name: "x", Argv: []string{SharingPath, "-a", "/Users/me/My Backups", "-S", "Backups"}, NeedsRoot: true}
	got := s.String()
	if !strings.HasPrefix(got, "sudo "+SharingPath) {
		t.Fatalf("unexpected rendering: %s", got)
	}
	if !strings.Contains(got, `'/Users/me/My Backups'`) {
		t.Errorf("a path with a space was not quoted: %s", got)
	}
}
