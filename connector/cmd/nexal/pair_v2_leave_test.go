package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"nexal/connector/internal/config"
)

func savedUnenrolledConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private", "config.json")
	c := config.Config{
		Version: 1, Coordinator: "http://127.0.0.1:1", Name: "M2 mini", Listen: "127.0.0.1:8788",
		Development: true, Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300,
	}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLeavePairV2UnenrolledReportsLeftWithoutNetwork(t *testing.T) {
	path := savedUnenrolledConfig(t)
	var err error
	out := captureStdout(t, func() { err = leavePairV2(context.Background(), path) })
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("bad JSON %q: %v", out, jerr)
	}
	if got["left"] != true {
		t.Fatalf("left = %v, want true", got["left"])
	}
	if _, ok := got["alreadyRevoked"]; ok {
		t.Fatalf("unenrolled leave must not claim a coordinator revocation: %v", got)
	}
	if _, serr := os.Stat(stopPath(path)); !os.IsNotExist(serr) {
		t.Fatalf("no recovery session was active, but a stop sentinel exists (err=%v)", serr)
	}
}

func TestLeavePairV2AsksActiveRecoverySessionToStop(t *testing.T) {
	path := savedUnenrolledConfig(t)
	if err := config.AtomicPrivate(sessionPath(path), []byte("{}")); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() { err = leavePairV2(context.Background(), path) })
	if err != nil {
		t.Fatal(err)
	}
	b, rerr := os.ReadFile(stopPath(path))
	if rerr != nil {
		t.Fatalf("stop sentinel not written: %v", rerr)
	}
	if string(b) != "network-leave\n" {
		t.Fatalf("sentinel = %q", b)
	}
}

func TestLeavePairV2MissingConfigErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	var err error
	captureStdout(t, func() { err = leavePairV2(context.Background(), path) })
	if err == nil {
		t.Fatal("expected an error for a missing configuration")
	}
}
