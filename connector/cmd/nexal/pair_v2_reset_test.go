package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/config"
)

func TestResetLocalPairV2UnenrolledReportsResetWithoutChangingConfig(t *testing.T) {
	path := savedUnenrolledConfig(t)
	var err error
	out := captureStdout(t, func() { err = resetLocalPairV2(context.Background(), path) })
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("bad JSON %q: %v", out, jerr)
	}
	if got["localEnrollmentReset"] != true {
		t.Fatalf("localEnrollmentReset = %v, want true", got["localEnrollmentReset"])
	}
	c, lerr := config.Load(path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if c.Enrollment != nil || c.HostID != "" {
		t.Fatalf("unenrolled reset changed enrollment state: %#v", c)
	}
}

func TestResetLocalPairV2RefusesPairedMac(t *testing.T) {
	path := savedUnenrolledConfig(t)
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c.Enrollment = &config.EnrollmentState{SchemaVersion: 2, SessionID: "123e4567-e89b-12d3-a456-426614174000", Status: "paired",
		AccountID: "a", NetworkID: "n", DeviceID: strings.Repeat("a", 64), PairedAt: "2026-01-01T00:00:00Z", ManagementURL: "https://127.0.0.1:1"}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	var rerr error
	out := captureStdout(t, func() { rerr = resetLocalPairV2(context.Background(), path) })
	if rerr == nil || !strings.Contains(rerr.Error(), "removed from the iPhone app") {
		t.Fatalf("paired reset error = %v, want iPhone removal guidance", rerr)
	}
	if out != "" {
		t.Fatalf("refused reset must not report success: %q", out)
	}
	after, lerr := config.Load(path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if after.Enrollment == nil || after.Enrollment.Status != "paired" {
		t.Fatalf("refused reset altered enrollment: %#v", after.Enrollment)
	}
}

func TestResetLocalPairV2MissingConfigErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	var err error
	captureStdout(t, func() { err = resetLocalPairV2(context.Background(), path) })
	if err == nil {
		t.Fatal("reset with no config must error")
	}
}
