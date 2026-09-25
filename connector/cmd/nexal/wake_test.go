package main

import (
	"context"
	"errors"
	"testing"

	"nexal/connector/internal/client"
)

func TestWakeErrorMessages(t *testing.T) {
	want := map[int]string{
		404: "that Mac has not reported Wake-on-LAN details yet, or is not on your network",
		409: "no other neXal Mac on that Mac's local network is awake to send the wake packet",
		429: "too many wake requests; wait a minute",
		503: "wake is temporarily unavailable",
	}
	for code, msg := range want {
		if got := wakeError(&client.StatusError{Status: code}).Error(); got != msg {
			t.Errorf("%d: %q", code, got)
		}
	}
	other := &client.StatusError{Status: 400}
	if wakeError(other) != error(other) {
		t.Fatal("unmapped status must pass through")
	}
	plain := errors.New("x")
	if wakeError(plain) != plain {
		t.Fatal("non-status error must pass through")
	}
}

func TestWakeFlagValidation(t *testing.T) {
	ctx := context.Background()
	for _, args := range [][]string{
		{"--config", "/tmp/nexal-none/config.json"},
		{"--tunnel", "100.64.0.1", "--host", "h1", "--config", "/tmp/nexal-none/config.json"},
		{"--tunnel", "192.168.1.2", "--config", "/tmp/nexal-none/config.json"},
		{"--tunnel", "100.128.0.1", "--config", "/tmp/nexal-none/config.json"},
		{"--host", "../x", "--config", "/tmp/nexal-none/config.json"},
	} {
		if err := wakeCommand(ctx, args); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
