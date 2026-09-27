package main

import (
	"context"
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
