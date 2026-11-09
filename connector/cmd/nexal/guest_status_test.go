package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"nexal/connector/internal/config"
)

func TestEmitGuestStatusReportsLifecycle(t *testing.T) {
	now := time.Now().UTC()
	valid := &config.GuestAccess{
		GrantID:         "grant-1",
		ReceivedAt:      now.Add(-time.Minute).Format(time.RFC3339Nano),
		AccessExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339Nano),
		InviterEmail:    "inviter@example.com",
	}
	expired := *valid
	expired.Expired = true
	cases := []struct {
		name  string
		guest *config.GuestAccess
		want  string
	}{
		{"no guest access", nil, "none"},
		{"valid lease, not yet paired", valid, "awaiting_guard"},
		{"lease flagged expired", &expired, "expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private", "config.json")
			c := config.Config{
				Version: 1, Coordinator: "http://127.0.0.1:1", Name: "M2 mini", Listen: "127.0.0.1:8788",
				Development: true, Paused: true, MemoryLimitBytes: 256 << 20,
				ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300, GuestAccess: tc.guest,
			}
			if err := config.Save(path, c); err != nil {
				t.Fatal(err)
			}
			var emitErr error
			out := captureStdout(t, func() { emitErr = emitGuestStatus(path) })
			if emitErr != nil {
				t.Fatal(emitErr)
			}
			var got struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("bad JSON %q: %v", out, err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %q, want %q", got.Status, tc.want)
			}
		})
	}
}

func TestEmitGuestStatusMissingConfigErrors(t *testing.T) {
	if err := emitGuestStatus(filepath.Join(t.TempDir(), "private", "config.json")); err == nil {
		t.Fatal("missing config was not reported")
	}
}
