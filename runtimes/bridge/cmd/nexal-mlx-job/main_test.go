package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	bridge "nexal/runtimebridge"
)

func TestCLIRejectsWithoutLeakingArguments(t *testing.T) {
	for _, args := range [][]string{
		nil, {"--unknown-SECRET_PROMPT"}, {"--max-tokens", "SECRET_PROMPT"},
		{"--timeout", "SECRET_PROMPT"}, {"SECRET_PROMPT"},
		{"--owner-policy", "/private/SECRET_PROMPT"},
	} {
		var out, diagnostics bytes.Buffer
		code := run(context.Background(), args, &out, &diagnostics)
		if code != 2 || out.Len() != 0 || strings.Contains(diagnostics.String(), "SECRET") ||
			!strings.Contains(diagnostics.String(), "INPUT_REJECTED") {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), diagnostics.String())
		}
	}
}

func TestCLIHelp(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if run(context.Background(), []string{"--help"}, &out, &diagnostics) != 0 ||
		!strings.Contains(out.String(), "Darwin arm64 only") || diagnostics.Len() != 0 {
		t.Fatal("help failed")
	}
}

func TestCLIErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   string
		status int
	}{
		{context.Canceled, "CANCELED", 130},
		{context.DeadlineExceeded, "DEADLINE_EXCEEDED", 124},
		{bridge.ErrPlatform, "UNSUPPORTED_PLATFORM", 2},
		{bridge.ErrPolicy, "OWNER_POLICY_REJECTED", 2},
		{bridge.ErrAdmission, "ADMISSION_REJECTED", 2},
		{bridge.ErrResult, "RESULT_REJECTED", 1},
		{bridge.ErrOutputLimit, "OUTPUT_LIMIT_EXCEEDED", 1},
		{bridge.ErrBusy, "LOCAL_RUNTIME_BUSY", 1},
		{errors.New("SECRET_PROMPT /private/secret"), "LOCAL_INFERENCE_FAILED", 1},
	} {
		var diagnostics bytes.Buffer
		status := emitError(&diagnostics, tc.err)
		if status != tc.status || !strings.Contains(diagnostics.String(), tc.code) ||
			strings.Contains(diagnostics.String(), "SECRET") {
			t.Fatalf("status=%d output=%q", status, diagnostics.String())
		}
	}
}
