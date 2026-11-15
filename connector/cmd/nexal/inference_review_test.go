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

func TestReadReviewedRejectsDirectoryAndCorruptFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := readReviewed(dir); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("a directory must be refused as not a file: %v", err)
	}
	bad := filepath.Join(dir, "reviewed-pins.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readReviewed(bad); err == nil {
		t.Fatal("corrupt pins JSON must be refused")
	}
	// The review command reports the same failure as invalid_arguments and leaves the file alone.
	cfg := filepath.Join(dir, "config.json")
	err := inferenceReview(context.Background(), []string{"qwen3-4b-q4", "--revision", strings.Repeat("a", 40), "--out", bad, "--config", cfg})
	if err == nil || errCode(err) != "invalid_arguments" {
		t.Fatalf("want invalid_arguments, got %v", err)
	}
	if b, _ := os.ReadFile(bad); string(b) != "{not json" {
		t.Fatal("the file was changed by a refused review")
	}
}
