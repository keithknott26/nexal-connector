package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/inference"
)

// seedInstalledModel writes a minimal receipt plus matching files so the verify
// and remove commands can run offline against a real on-disk layout.
func seedInstalledModel(t *testing.T, cfg, id string) (modelDir, receipt string) {
	t.Helper()
	infDir := inferenceDirOf(cfg)
	modelDir = filepath.Join(infDir, "models", id)
	receipts := filepath.Join(infDir, "receipts")
	for _, d := range []string{modelDir, receipts} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	put := func(name, body string) inference.PinFile {
		if err := os.WriteFile(filepath.Join(modelDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		return inference.PinFile{Name: name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
	}
	cfgFile := put("config.json", `{"a":1}`)
	manifest := put(inference.ManifestFileName, `{"m":1}`)
	rc := inference.Receipt{
		SchemaVersion:  inference.ReceiptSchemaVersion,
		ModelID:        id,
		State:          "ready",
		ManifestSHA256: manifest.SHA256,
		ManifestBytes:  manifest.Size,
		Files:          []inference.PinFile{cfgFile},
	}
	data, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	receipt = filepath.Join(receipts, id+".json")
	if err := os.WriteFile(receipt, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return modelDir, receipt
}

func TestInferenceVerifyReportsOKThenDamagedAfterTamper(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	ctx := context.Background()
	modelDir, _ := seedInstalledModel(t, cfg, "qwen3-4b-q4")

	var err error
	out := captureStdout(t, func() {
		err = inferenceCommand(ctx, []string{"verify", "qwen3-4b-q4", "--config", cfg})
	})
	if err != nil || !strings.Contains(out, "every file matches its install receipt") {
		t.Fatalf("clean verify: err=%v out=%q", err, out)
	}

	if err := os.WriteFile(filepath.Join(modelDir, "config.json"), []byte(`{"a":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() {
		err = inferenceCommand(ctx, []string{"verify", "qwen3-4b-q4", "--config", cfg})
	})
	if errCode(err) != "verify_failed" || !strings.Contains(out, "DAMAGED") || !strings.Contains(out, "hash_mismatch") {
		t.Fatalf("tampered verify: err=%v out=%q", err, out)
	}

	out = captureStdout(t, func() {
		err = inferenceCommand(ctx, []string{"verify", "qwen3-4b-q4", "--json", "--config", cfg})
	})
	var res inference.VerifyResult
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil || res.OK || errCode(err) != "verify_failed" {
		t.Fatalf("json verify: err=%v jsonErr=%v out=%q", err, jerr, out)
	}
}

func TestInferenceRemoveDeletesModelAndReceipt(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	ctx := context.Background()
	modelDir, receipt := seedInstalledModel(t, cfg, "qwen3-4b-q4")

	var err error
	out := captureStdout(t, func() {
		err = inferenceCommand(ctx, []string{"remove", "qwen3-4b-q4", "--yes", "--config", cfg})
	})
	if err != nil || strings.Count(out, "removed ") != 2 {
		t.Fatalf("remove: err=%v out=%q", err, out)
	}
	for _, p := range []string{modelDir, receipt} {
		if _, serr := os.Lstat(p); !os.IsNotExist(serr) {
			t.Errorf("%s still exists (%v)", p, serr)
		}
	}
	if err := inferenceCommand(ctx, []string{"remove", "qwen3-4b-q4", "--yes", "--config", cfg}); errCode(err) != "not_installed" {
		t.Fatalf("second remove: %v", err)
	}
}
