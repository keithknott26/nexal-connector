package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setup must refuse non-Apple-silicon hosts with a coded unsupported_platform error
// (plain and --json) before it creates the inference directory or runs anything.
func TestInferenceSetupRefusesUnsupportedPlatformWithoutSideEffects(t *testing.T) {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		t.Skip("platform is supported; setup would run for real")
	}
	for _, extra := range [][]string{nil, {"--json"}} {
		dir := t.TempDir()
		args := append([]string{"--config", filepath.Join(dir, "config.json")}, extra...)
		err := inferenceSetup(context.Background(), args)
		var ce *codedError
		if !errors.As(err, &ce) || ce.code != "unsupported_platform" {
			t.Fatalf("args %v: got %#v, want coded unsupported_platform", args, err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "inference")); !os.IsNotExist(statErr) {
			t.Fatalf("args %v: inference directory created on unsupported platform (%v)", args, statErr)
		}
	}
}
