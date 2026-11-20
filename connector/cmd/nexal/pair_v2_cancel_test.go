package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"nexal/connector/internal/config"
)

const cancelTestSession = "123e4567-e89b-12d3-a456-426614174000"
const cancelTestToken = "enrollment-poll-token-0123456789"

// cancelConfigWithCoordinator saves an unenrolled config pointing at the given
// coordinator and stores the session's poll credential in a private secrets dir.
func cancelConfigWithCoordinator(t *testing.T, coordinator string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	secretsDir := t.TempDir()
	if err := os.Chmod(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXAL_SECRETS_DIR", secretsDir)
	path := savedUnenrolledConfig(t)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Coordinator = coordinator
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	secrets, err := config.NewSecrets(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(context.Background(), "enrollment-session-"+cancelTestSession, cancelTestToken); err != nil {
		t.Fatal(err)
	}
	return path
}

// A confirmed cancellation posts to the session's cancel endpoint with the
// stored poll credential and reports enrollmentCancelled.
func TestCancelPairV2ConfirmedPrintsCancelled(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"cancelled":true}`))
	}))
	defer srv.Close()
	path := cancelConfigWithCoordinator(t, srv.URL)
	var err error
	out := captureStdout(t, func() { err = cancelPairV2(context.Background(), path, cancelTestSession) })
	if err != nil {
		t.Fatal(err)
	}
	if want := "/api/v2/enrollment-sessions/" + cancelTestSession + "/cancel"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if gotAuth != "Bearer "+cancelTestToken {
		t.Fatalf("authorization = %q", gotAuth)
	}
	var got map[string]any
	if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
		t.Fatalf("bad JSON %q: %v", out, jerr)
	}
	if got["enrollmentCancelled"] != true || got["sessionId"] != cancelTestSession {
		t.Fatalf("output = %v", got)
	}
}

// If the coordinator does not confirm, the command fails and prints no success.
func TestCancelPairV2UnconfirmedFailsWithoutOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"cancelled":false}`))
	}))
	defer srv.Close()
	path := cancelConfigWithCoordinator(t, srv.URL)
	var err error
	out := captureStdout(t, func() { err = cancelPairV2(context.Background(), path, cancelTestSession) })
	if err == nil || !strings.Contains(err.Error(), "did not confirm") {
		t.Fatalf("err = %v, want unconfirmed cancellation", err)
	}
	if out != "" {
		t.Fatalf("unexpected output %q", out)
	}
}
