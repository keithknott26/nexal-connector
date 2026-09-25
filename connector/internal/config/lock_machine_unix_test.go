//go:build darwin || linux

package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigIn creates a private config.json under its own directory, mirroring the two
// profiles the macOS app maintains.
func writeConfigIn(t *testing.T, home, dir string) string {
	t.Helper()
	d := filepath.Join(home, dir)
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "config.json")
	if err := os.WriteFile(p, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The regression this exists for. Two different configurations must NOT both run.
//
// Before LockMachine, config.Lock was the only guard and it is scoped to the config
// file's directory, so production and the app's development profile each took their own
// lock and ran simultaneously. This asserts the second one is refused.
func TestLockMachineRefusesDevelopmentProfileAlongsideProduction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	prod := writeConfigIn(t, home, filepath.Join("Library", "Application Support", "Nexal"))
	dev := writeConfigIn(t, home, filepath.Join("Library", "Application Support", "Nexal-Development"))

	// Both per-config locks still succeed; that is exactly why a host-wide one is needed.
	u1, err := Lock(prod)
	if err != nil {
		t.Fatalf("per-config lock on production: %v", err)
	}
	defer u1()
	u2, err := Lock(dev)
	if err != nil {
		t.Fatalf("per-config lock on development should still succeed independently: %v", err)
	}
	defer u2()

	release, err := LockMachine(prod)
	if err != nil {
		t.Fatalf("first host lock: %v", err)
	}
	defer release()

	_, err = LockMachine(dev)
	if err == nil {
		t.Fatal("development profile acquired the host lock while production held it -- " +
			"the duplicate-connector bug is not fixed")
	}
	// The message must identify the holder, not just report a conflict: two identical
	// connectors were what made the original report hard to diagnose.
	if !strings.Contains(err.Error(), prod) {
		t.Errorf("error should name the holding configuration, got: %v", err)
	}
	if !strings.Contains(err.Error(), "pid ") {
		t.Errorf("error should name the holding pid, got: %v", err)
	}
}

// The lock path must not vary with the configuration, since deriving it from the config
// path is precisely how the original bug arose.
func TestLockMachinePathIsIndependentOfConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prod := writeConfigIn(t, home, filepath.Join("Library", "Application Support", "Nexal"))
	dev := writeConfigIn(t, home, filepath.Join("Library", "Application Support", "Nexal-Development"))

	for _, cfg := range []string{prod, dev} {
		release, err := LockMachine(cfg)
		if err != nil {
			t.Fatalf("lock with %s: %v", cfg, err)
		}
		release()
	}
	// Exactly one lock file, and never one inside the development directory.
	//
	// The directory is asked of the code rather than spelled out: it is the macOS
	// Application Support path on darwin and ~/.config/nexal elsewhere, and hardcoding
	// either makes the test assert the platform instead of the property.
	dir, err := machineStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, machineLockName)); err != nil {
		t.Errorf("expected a single host lock in the state dir: %v", err)
	}
	stray := filepath.Join(home, "Library", "Application Support", "Nexal-Development", machineLockName)
	if _, err := os.Stat(stray); err == nil {
		t.Error("a per-profile lock file was created; the path is still config-derived")
	}
}

// Release must let the next connector start, or a restart would be permanently wedged.
func TestLockMachineReleaseAllowsRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := writeConfigIn(t, home, filepath.Join("Library", "Application Support", "Nexal"))

	release, err := LockMachine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	release()

	again, err := LockMachine(cfg)
	if err != nil {
		t.Fatalf("restart after clean release was refused: %v", err)
	}
	defer again()

	// A released lock must not still name a dead process's configuration.
	dir, err := machineStateDir()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, machineLockName))
	if strings.Count(string(data), "config ") > 1 {
		t.Errorf("stale holder records accumulated: %q", string(data))
	}
}

// A group- or world-accessible lock file is refused, matching Lock()'s stance: the file
// records an absolute configuration path.
func TestLockMachineRejectsPermissiveLockFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := writeConfigIn(t, home, filepath.Join("Library", "Application Support", "Nexal"))

	dir, err := machineStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(dir, machineLockName)
	// Chmod after create: os.WriteFile's mode is masked by umask, so 0666 would land as
	// 0644 and the test would pass for the wrong reason on some machines.
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := LockMachine(cfg); err == nil {
		t.Error("a world-writable host lock was accepted")
	}
}

// An empty lock file must degrade to a plain refusal rather than a malformed one.
func TestLockMachineHandlesEmptyHolderRecord(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := writeConfigIn(t, home, filepath.Join("Library", "Application Support", "Nexal"))

	release, err := LockMachine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	dir, err := machineStateDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(dir, machineLockName), 0); err != nil {
		t.Fatal(err)
	}
	_, err = LockMachine(cfg)
	if err == nil {
		t.Fatal("expected refusal even with no holder record")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("unhelpful message with an empty holder record: %v", err)
	}
}

// Cross-process proof.
//
// The tests above call LockMachine twice inside one process. flock is associated with
// the open file description, so two separate opens conflict even in-process -- but the
// bug being fixed was two separate PROCESSES, and asserting the in-process case alone
// would leave the real claim untested. This re-executes the test binary as a child that
// tries to take the lock the parent already holds.
func TestLockMachineExcludesASecondProcess(t *testing.T) {
	if cfg := os.Getenv("NEXAL_TEST_CHILD_LOCK_CONFIG"); cfg != "" {
		// Child: HOME is inherited from the parent, so it resolves the same state dir.
		if _, err := LockMachine(cfg); err != nil {
			os.Stderr.WriteString("CHILD_REFUSED: " + err.Error())
			os.Exit(3)
		}
		os.Exit(0) // acquired -- the parent treats this as a failure
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	prod := writeConfigIn(t, home, filepath.Join("Library", "Application Support", "Nexal"))
	dev := writeConfigIn(t, home, filepath.Join("Library", "Application Support", "Nexal-Development"))

	release, err := LockMachine(prod)
	if err != nil {
		t.Fatalf("parent could not take the host lock: %v", err)
	}
	defer release()

	// The child uses the DEVELOPMENT configuration: the exact pairing that previously
	// allowed two connectors to run at once.
	cmd := exec.Command(os.Args[0], "-test.run", "TestLockMachineExcludesASecondProcess")
	cmd.Env = append(os.Environ(),
		"NEXAL_TEST_CHILD_LOCK_CONFIG="+dev,
		"HOME="+home,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("a second PROCESS acquired the host lock using the development profile")
	}
	if !strings.Contains(string(out), "CHILD_REFUSED") {
		t.Fatalf("child failed for some other reason: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "already running") {
		t.Errorf("child was refused without the expected explanation: %s", out)
	}
	// It must name the parent's pid, not the child's.
	if !strings.Contains(string(out), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Errorf("refusal did not identify the holding process (parent pid %d): %s", os.Getpid(), out)
	}
}

// Leaving the network stops the running agent by the pid it recorded, so that pid
// must be reported only while the agent really holds the lock.
func TestRunningAgentReportsOnlyALiveHolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prod := writeConfigIn(t, home, filepath.Join("Library", "Application Support", "Nexal"))

	if _, _, ok := RunningAgent(); ok {
		t.Fatal("no agent has run yet, but one was reported")
	}
	unlock, err := LockMachine(prod)
	if err != nil {
		t.Fatal(err)
	}
	pid, cfg, ok := RunningAgent()
	if !ok || pid != os.Getpid() || cfg != prod {
		t.Fatalf("RunningAgent() = %d, %q, %v; want %d, %q, true", pid, cfg, ok, os.Getpid(), prod)
	}
	unlock()
	if _, _, ok := RunningAgent(); ok {
		t.Fatal("a released lock must not report a running agent")
	}
}
