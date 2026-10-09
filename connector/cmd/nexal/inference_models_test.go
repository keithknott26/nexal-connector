package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func errCode(err error) string {
	var c *codedError
	if errors.As(err, &c) {
		return c.code
	}
	return ""
}

func TestParseModelArgs(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	def := func(f *flag.FlagSet) { f.Bool("json", false, ""); f.String("revision", "", "") }
	tests := []struct {
		name   string
		args   []string
		needID bool
		wantID string
		bad    bool
	}{
		{"id first", []string{"qwen3-4b-q4", "--json", "--config", cfg}, true, "qwen3-4b-q4", false},
		{"id after flags", []string{"--json", "--config", cfg, "qwen3-4b-q4"}, true, "qwen3-4b-q4", false},
		{"id between", []string{"qwen3-4b-q4", "--revision", "abc", "--config", cfg}, true, "qwen3-4b-q4", false},
		{"missing id", []string{"--json", "--config", cfg}, true, "", true},
		{"two ids", []string{"a", "b", "--config", cfg}, true, "", true},
		{"unknown flag", []string{"a", "--nope", "--config", cfg}, true, "", true},
		{"relative config", []string{"a", "--config", "rel/config.json"}, true, "", true},
		{"no id wanted, one given", []string{"extra", "--config", cfg}, false, "", true},
		{"no id wanted", []string{"--json", "--config", cfg}, false, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, _, _, err := parseModelArgs("x", tc.args, tc.needID, def)
			if (err != nil) != tc.bad {
				t.Fatalf("err = %v", err)
			}
			if err != nil && errCode(err) != "invalid_arguments" {
				t.Errorf("code = %q", errCode(err))
			}
			if id != tc.wantID {
				t.Errorf("id = %q", id)
			}
		})
	}
}

func TestInferenceDispatchAndStatusRemoveFlow(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	ctx := context.Background()
	if err := inferenceCommand(ctx, nil); err == nil {
		t.Error("no subcommand accepted")
	}
	if err := inferenceCommand(ctx, []string{"bogus"}); err == nil {
		t.Error("unknown subcommand accepted")
	}
	out := captureStdout(t, func() {
		if err := inferenceCommand(ctx, []string{"status", "--json", "--config", cfg}); err != nil {
			t.Error(err)
		}
	})
	var st map[string]any
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status output %q: %v", out, err)
	}
	if st["schemaVersion"] != float64(1) || len(st["models"].([]any)) != 0 || len(st["incomplete"].([]any)) != 0 {
		t.Errorf("status = %v", st)
	}
	// remove needs --yes and a valid id; nothing to remove is an error.
	if err := inferenceCommand(ctx, []string{"remove", "qwen3-4b-q4", "--config", cfg}); errCode(err) != "confirmation_required" {
		t.Errorf("remove without --yes: %v", err)
	}
	if err := inferenceCommand(ctx, []string{"remove", "../x", "--yes", "--config", cfg}); errCode(err) != "invalid_arguments" {
		t.Errorf("remove escape: %v", err)
	}
	if err := inferenceCommand(ctx, []string{"remove", "qwen3-4b-q4", "--yes", "--config", cfg}); errCode(err) != "not_installed" {
		t.Errorf("remove nothing: %v", err)
	}
	if err := inferenceCommand(ctx, []string{"verify", "qwen3-4b-q4", "--config", cfg}); errCode(err) != "not_installed" {
		t.Errorf("verify nothing: %v", err)
	}
}

func TestPinAndInstallRefuseBeforeAnyNetwork(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	ctx := context.Background()
	for _, rev := range []string{"main", "refs/pr/1", "abc123"} {
		err := inferenceCommand(ctx, []string{"pin", "qwen3-4b-q4", "--revision", rev, "--config", cfg})
		if errCode(err) != "mutable_revision" {
			t.Errorf("pin --revision %s: %v", rev, err)
		}
	}
	if err := inferenceCommand(ctx, []string{"pin", "qwen3-4b-q4", "--config", cfg}); errCode(err) != "invalid_arguments" {
		t.Errorf("pin without revision: %v", err)
	}
	// install of an unpinned model fails before the host check or network, with an error event.
	out := captureStdout(t, func() {
		err := inferenceCommand(ctx, []string{"install", "qwen3-4b-q4", "--json", "--config", cfg})
		if errCode(err) != "not_installable" {
			t.Errorf("install unpinned: %v", err)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("events = %q", out)
	}
	var ev map[string]any
	json.Unmarshal([]byte(lines[0]), &ev)
	if ev["event"] != "error" || ev["code"] != "not_installable" || !strings.Contains(ev["message"].(string), "not pinned") || ev["fix"] == "" {
		t.Errorf("event = %v", ev)
	}
}

// The documented argv puts the model id FIRST and the flags after it; the stdlib
// flag package stops at the first positional, so the id is peeled off first.
func TestIDFirstThenFlagsParsesForEveryCommand(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	for _, cmd := range []string{"install", "verify", "remove", "pin"} {
		var asJSON *bool
		var rev *string
		id, _, path, err := parseModelArgs(cmd, []string{"qwen3-4b-q4", "--json", "--revision", "abc", "--config", cfg}, true, func(f *flag.FlagSet) {
			asJSON = f.Bool("json", false, "")
			rev = f.String("revision", "", "")
		})
		if err != nil || id != "qwen3-4b-q4" || !*asJSON || *rev != "abc" || *path != cfg {
			t.Errorf("%s: id=%q json=%v rev=%q path=%q err=%v", cmd, id, asJSON != nil && *asJSON, *rev, *path, err)
		}
	}
}
