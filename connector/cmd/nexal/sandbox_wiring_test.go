package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestEnvOrFallsBackOnUnsetAndEmpty(t *testing.T) {
	const name = "NEXAL_TEST_ENVOR"
	t.Setenv(name, "")
	if got := envOr(name, "fallback"); got != "fallback" {
		t.Fatalf("empty env = %q, want fallback", got)
	}
	t.Setenv(name, "/custom")
	if got := envOr(name, "fallback"); got != "/custom" {
		t.Fatalf("set env = %q, want /custom", got)
	}
}

func TestCodedErrorUnwrapsAndPlainErrorsKeepDefaultCode(t *testing.T) {
	root := errors.New("boom")
	coded := &codedError{code: "some_code", err: root}
	if !errors.Is(coded, root) {
		t.Fatal("codedError must unwrap to its cause")
	}
	if got := errorCode(fmt.Errorf("wrapped: %w", coded)); got != "some_code" {
		t.Fatalf("wrapped coded error code = %q", got)
	}
	if got := errorCode(root); got != "connector_error" {
		t.Fatalf("plain error code = %q, want connector_error", got)
	}
}
