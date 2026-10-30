package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestInferenceReviewed(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")

	t.Run("json document has schema version and models", func(t *testing.T) {
		var err error
		out := captureStdout(t, func() { err = inferenceReviewed(context.Background(), []string{"--config", cfg, "--json"}) })
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			SchemaVersion int               `json:"schemaVersion"`
			Models        []json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("not one JSON document: %v\n%s", err, out)
		}
		if doc.SchemaVersion != 1 {
			t.Fatalf("schemaVersion %d, want 1", doc.SchemaVersion)
		}
	})
	t.Run("text output succeeds", func(t *testing.T) {
		var err error
		out := captureStdout(t, func() { err = inferenceReviewed(context.Background(), []string{"--config", cfg}) })
		if err != nil {
			t.Fatal(err)
		}
		if out == "" {
			t.Fatal("expected output")
		}
	})
	t.Run("positional arguments rejected", func(t *testing.T) {
		err := inferenceReviewed(context.Background(), []string{"--config", cfg, "extra"})
		if errCode(err) != "invalid_arguments" {
			t.Fatalf("err %v, want invalid_arguments", err)
		}
	})
	t.Run("relative config rejected", func(t *testing.T) {
		err := inferenceReviewed(context.Background(), []string{"--config", "config.json"})
		if errCode(err) != "invalid_arguments" {
			t.Fatalf("err %v, want invalid_arguments", err)
		}
	})
}
