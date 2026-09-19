package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func savedConfig() Config {
	return Config{Version: 1, Coordinator: "https://coordinator.example", Name: "test",
		Listen: "127.0.0.1:8788", Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
}

func TestLoadRejectsAmbiguousConsent(t *testing.T) {
	b, err := json.Marshal(savedConfig())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"duplicate_pause": strings.Replace(string(b), `"paused":true`, `"paused":true,"paused":false`, 1),
		"case_alias":      strings.Replace(string(b), `"paused":true`, `"paused":true,"PAUSED":false`, 1),
		"escaped_alias":   strings.Replace(string(b), `"paused":true`, `"paused":true,"paus\u0065d":false`, 1),
		"null_pause":      strings.Replace(string(b), `"paused":true`, `"paused":null`, 1),
		"missing_pause":   strings.Replace(string(b), `"paused":true,`, "", 1),
		"null_mode":       strings.Replace(string(b), `"development":false`, `"development":null`, 1),
		"null_root":       "null",
		"nested_duplicate": strings.TrimSuffix(string(b), "}") +
			`,"tunnel":{"sha256":"first","SHA256":"second"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private", "config.json")
			if err := AtomicPrivate(path, []byte(body)); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("ambiguous or missing consent accepted")
			}
		})
	}
	for _, paused := range []bool{true, false} {
		c := savedConfig()
		c.Paused = paused
		path := filepath.Join(t.TempDir(), "private", "config.json")
		if err := Save(path, c); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || got.Paused != paused {
			t.Fatalf("valid explicit consent did not round trip: %v", err)
		}
	}
}

func TestPrivateReadRedactsPathAndPreservesMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sensitive-owner-secret", "missing")
	_, err := ReadPrivate(path, 100)
	if err == nil || strings.Contains(err.Error(), "sensitive-owner-secret") ||
		strings.Contains(err.Error(), path) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private read lost redaction or missing-file semantics: %v", err)
	}
}

func TestReadPrivateRejectsSpecialFilesAndInvalidBounds(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivate(fifo, 100); err == nil {
		t.Fatal("FIFO accepted")
	}
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{-1, 1<<63 - 1, 3} {
		if _, err := ReadPrivate(path, limit); err == nil {
			t.Fatal("invalid bound accepted")
		}
	}
	if _, err := ReadPrivate("relative", 100); err == nil {
		t.Fatal("relative private read accepted")
	}
}

func TestJSONObjectsRejectAmbiguityWithoutEcho(t *testing.T) {
	for _, body := range []string{
		`{"secret":"do-not-echo","SECRET":"overwrite"}`,
		`{"paused":true,"pauſed":false}`,
		`{"items":[{"a":1,"a":2}]}`,
		`{"a":1}{"b":2}`, `[]`, `null`, `{"x":`, `{"x":]}`,
		`{"x":` + strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65) + "}",
	} {
		err := CheckJSONObject([]byte(body))
		if err == nil || strings.Contains(err.Error(), "do-not-echo") {
			t.Fatal("ambiguous JSON accepted or echoed")
		}
	}
	for _, body := range []string{`{}`, `{"attempt":null}`, `{"nested":{"a":1},"array":[true,false,null]}`} {
		if err := CheckJSONObject([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
}
