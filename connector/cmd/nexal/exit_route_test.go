package main

import (
	"context"
	"errors"
	"nexal/connector/internal/client"
	"strings"
	"testing"
)

func TestExitRouteRejectsUnsafeFlagsBeforeCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"--tunnel", "8.8.8.8"},
		{"--target-device", "22222222-2222-4222-8222-222222222222"},
		{"--disable", "--target-device", "bad-id"},
		{"--disable"},
	} {
		args = append(args, "--config", "/private/tmp/nonexistent-nexal-test-config.json")
		if err := exitRouteCommand(context.Background(), args); err == nil || strings.Contains(err.Error(), "configuration") {
			t.Fatalf("invalid flags not rejected before reading config: %v: %v", args, err)
		}
	}
}

func TestExitRoutePendingIsNotReportedAsConfigured(t *testing.T) {
	err := exitRouteError(&client.StatusError{Status: 409, Code: "exit_route_pending"})
	if errorCode(err) != "exit_route_pending" || !strings.Contains(err.Error(), "still being applied") {
		t.Fatal("pending state not explained", err)
	}
}

func TestExitRouteErrorMapsKnownCodesAndPassesOthersThrough(t *testing.T) {
	for _, code := range []string{
		"exit_source_unavailable",
		"exit_target_unavailable",
		"exit_peer_mapping_pending",
		"exit_route_unavailable",
		"exit_route_pending",
		"exit_route_superseded",
	} {
		raw := &client.StatusError{Status: 409, Code: code}
		err := exitRouteError(raw)
		if errorCode(err) != code || err.Error() == raw.Error() || err.Error() == "" {
			t.Fatalf("%s not mapped to a friendly coded error: %v", code, err)
		}
	}
	unknown := &client.StatusError{Status: 500, Code: "something_else"}
	if got := exitRouteError(unknown); got != error(unknown) {
		t.Fatalf("unknown status code was rewritten: %v", got)
	}
	plain := errors.New("dial failed")
	if got := exitRouteError(plain); got != plain {
		t.Fatalf("non-status error was rewritten: %v", got)
	}
}
