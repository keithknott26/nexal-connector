package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"nexal/connector/internal/client"
)

func TestSandboxErrorMapsCodesAndStatuses(t *testing.T) {
	codeOf := func(err error) string {
		var c *codedError
		if errors.As(err, &c) {
			return c.code
		}
		return ""
	}
	// Known code -> plain-words text, code preserved.
	err := sandboxError(&client.StatusError{Status: 409, Code: "runner_full"})
	if codeOf(err) != "runner_full" || err.Error() != sandboxErrorText["runner_full"] {
		t.Fatalf("known code: %v", err)
	}
	// Unknown code keeps the original error and appends the code.
	err = sandboxError(&client.StatusError{Status: 400, Code: "weird_code"})
	if codeOf(err) != "weird_code" || !strings.Contains(err.Error(), "weird_code") {
		t.Fatalf("unknown code: %v", err)
	}
	// Wrapped status errors are still recognised.
	err = sandboxError(fmt.Errorf("wrap: %w", &client.StatusError{Status: 400, Code: "plan_required"}))
	if codeOf(err) != "plan_required" {
		t.Fatalf("wrapped: %v", err)
	}
	// Code-less statuses.
	if got := codeOf(sandboxError(&client.StatusError{Status: 404})); got != "sandbox_not_found" {
		t.Fatalf("404 -> %q", got)
	}
	if got := codeOf(sandboxError(&client.StatusError{Status: 429})); got != "rate_limited" {
		t.Fatalf("429 -> %q", got)
	}
	if got := codeOf(sandboxError(&client.StatusError{Status: 501})); got != "sandboxes_unavailable" {
		t.Fatalf("501 -> %q", got)
	}
	// Unrelated errors pass through untouched.
	plain := errors.New("boom")
	if sandboxError(plain) != plain {
		t.Fatal("plain error should pass through")
	}
}
