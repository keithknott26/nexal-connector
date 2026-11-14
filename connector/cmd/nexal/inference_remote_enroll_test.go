package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Every remote subcommand must stop at "not_enrolled" for an unenrolled host,
// before it asks the secrets store for a token or touches the network.
func TestInferenceRemoteRefusesUnenrolledHostBeforeCredentialAccess(t *testing.T) {
	path := bundleTestConfig(t, "")
	cases := map[string][]string{
		"remote-list":    {"remote-list", "--config", path},
		"remote-stats":   {"remote-stats", "--config", path},
		"remote-stop":    {"remote-stop", "model-x", "--config", path},
		"remote-serve":   {"remote-serve", "model-x", "--to", bundleTestID, "--config", path},
		"remote-offload": {"remote-offload", "model-x", "--mode", "off", "--config", path},
	}
	for name, args := range cases {
		err := inferenceRemote(context.Background(), args)
		var ce *codedError
		if !errors.As(err, &ce) || ce.code != "not_enrolled" {
			t.Fatalf("%s: expected not_enrolled, got %v", name, err)
		}
	}
}

// remote-offload validates its flags before reading the config, so a bad mode is
// reported as invalid_arguments even when the config file does not exist.
func TestInferenceRemoteOffloadRejectsBadModeBeforeReadingConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent", "config.json")
	err := inferenceRemote(context.Background(), []string{"remote-offload", "model-x", "--mode", "sometimes", "--config", missing})
	var ce *codedError
	if !errors.As(err, &ce) || ce.code != "invalid_arguments" {
		t.Fatalf("expected invalid_arguments, got %v", err)
	}
}
