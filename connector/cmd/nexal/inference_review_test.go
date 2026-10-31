package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewRefusesMutableRevisionAndWrongFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "reviewed-pins.json")
	if err := os.WriteFile(good, []byte(`{"schemaVersion":1,"pins":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.json")
	for name, args := range map[string][]string{
		"branch name":  {"qwen3-4b-q4", "--revision", "main", "--out", good, "--config", cfg},
		"short hash":   {"qwen3-4b-q4", "--revision", "abc123", "--out", good, "--config", cfg},
		"wrong file":   {"qwen3-4b-q4", "--revision", strings.Repeat("a", 40), "--out", filepath.Join(dir, "other.json"), "--config", cfg},
		"missing file": {"qwen3-4b-q4", "--revision", strings.Repeat("a", 40), "--out", filepath.Join(dir, "x", "reviewed-pins.json"), "--config", cfg},
	} {
		err := inferenceReview(context.Background(), args)
		if err == nil || errCode(err) != "invalid_arguments" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if b, _ := os.ReadFile(good); string(b) != `{"schemaVersion":1,"pins":{}}` {
		t.Fatal("the file was changed by a refused review")
	}
}

func TestReviewLatestRejectsBadArgsAndUnknownModel(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	for name, tc := range map[string]struct {
		args []string
		code string
	}{
		"no model id":     {[]string{"--config", cfg}, "invalid_arguments"},
		"relative config": {[]string{"qwen3-4b-q4", "--config", "config.json"}, "invalid_arguments"},
		"unknown model":   {[]string{"not-a-catalog-model", "--config", cfg}, "model_not_found"},
	} {
		err := inferenceReviewLatest(context.Background(), tc.args)
		if err == nil || errCode(err) != tc.code {
			t.Errorf("%s: want code %s, got %v", name, tc.code, err)
		}
	}
}
