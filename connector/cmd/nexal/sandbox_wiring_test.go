package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnvOrFallsBackOnUnsetAndEmpty(t *testing.T) {
	const name = "NEXAL_TEST_ENVOR"
	t.Setenv(name, "")
	if got := envOr(name, "fallback"); got != "fallback" {
		t.Fatalf("empty env = %q, want fallback", got)
	}
	t.Setenv(name, "/custom")
	if got := envOr(name, "fallback"); got != "/custom" {
		t.Fatalf("set env = %q, want /custom", got)
	}
}

func TestCodedErrorUnwrapsAndPlainErrorsKeepDefaultCode(t *testing.T) {
	root := errors.New("boom")
	coded := &codedError{code: "some_code", err: root}
	if !errors.Is(coded, root) {
		t.Fatal("codedError must unwrap to its cause")
	}
	if got := errorCode(fmt.Errorf("wrapped: %w", coded)); got != "some_code" {
		t.Fatalf("wrapped coded error code = %q", got)
	}
	if got := errorCode(root); got != "connector_error" {
		t.Fatalf("plain error code = %q, want connector_error", got)
	}
}

func TestNewSandboxManagerIsOffUnlessManagedRunnerOptedIn(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("exercises the Linux selection logic")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv(managedRunnerEnv, "")
	if m := newSandboxManager(logger); m != nil {
		t.Fatal("a Linux host without NEXAL_MANAGED_RUNNER=1 must not host sandboxes")
	}
	t.Setenv(managedRunnerEnv, "true") // only the exact value "1" opts in
	if m := newSandboxManager(logger); m != nil {
		t.Fatal("NEXAL_MANAGED_RUNNER must be exactly 1 to opt in")
	}
}

func TestNewManagedSandboxManagerBuildsFromEnvironment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed runner is Linux only")
	}
	dir := t.TempDir()
	t.Setenv(managedRunnerEnv, "1")
	t.Setenv(managedStateEnv, dir)
	t.Setenv(managedConfigEnv, filepath.Join(dir, "absent-sandbox-hosting.json"))
	t.Setenv(managedSliceEnv, "none")
	t.Setenv(managedPidsEnv, "not-a-number")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if m := newSandboxManager(logger); m == nil {
		t.Fatal("managed runner with a missing hosting config should still build a (disabled) manager")
	}
}
