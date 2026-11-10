package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/config"
)

const bundleTestID = "11111111-1111-4111-8111-111111111111"

func TestBundleCommandRejectsBadFlagsBeforeReadingConfig(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	cases := []struct {
		command string
		args    []string
	}{
		{"bundle-send", nil},
		{"bundle-send", []string{"--transfer", bundleTestID, "--bundle", "relative/dir", "--receiver-key-sha256", fingerprint}},
		{"bundle-send", []string{"--transfer", bundleTestID, "--bundle", "/tmp/b", "--receiver-key-sha256", "short"}},
		{"bundle-send", []string{"--transfer", bundleTestID, "--bundle", "/tmp/b", "--receiver-key-sha256", fingerprint, "--from-host", bundleTestID}},
		{"bundle-send", []string{"--transfer", bundleTestID, "--bundle", "/tmp/b", "--receiver-key-sha256", fingerprint, "--path-only"}},
		{"bundle-receive", nil},
		{"bundle-receive", []string{"--from-host", "bad id/.."}},
		{"bundle-receive", []string{"--from-host", bundleTestID, "--transfer", bundleTestID}},
		{"bundle-receive", []string{"--from-host", bundleTestID, "--bundle", "/tmp/b"}},
	}
	for _, tc := range cases {
		args := append(append([]string{}, tc.args...), "--config", "/private/tmp/nonexistent-nexal-test-config.json")
		err := bundleCommand(context.Background(), tc.command, args)
		if err == nil || !strings.Contains(err.Error(), tc.command+" requires") {
			t.Fatalf("%s %v: want flag validation error before config load, got %v", tc.command, tc.args, err)
		}
	}
}

func TestBundleCommandAcceptsValidFlagsThenFailsOnMissingConfig(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	missing := "/private/tmp/nonexistent-nexal-test-config.json"
	cases := []struct {
		command string
		args    []string
	}{
		{"bundle-send", []string{"--transfer", bundleTestID, "--bundle", "/tmp/b", "--receiver-key-sha256", fingerprint, "--config", missing}},
		{"bundle-receive", []string{"--from-host", bundleTestID, "--config", missing}},
		{"bundle-receive", []string{"--from-host", bundleTestID, "--path-only", "--config", missing}},
	}
	for _, tc := range cases {
		err := bundleCommand(context.Background(), tc.command, tc.args)
		if err == nil {
			t.Fatalf("%s %v: want an error for the missing config", tc.command, tc.args)
		}
		if strings.Contains(err.Error(), tc.command+" requires") {
			t.Fatalf("%s %v: valid flags must pass validation, got %v", tc.command, tc.args, err)
		}
	}
}

func bundleTestConfig(t *testing.T, hostID string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private", "config.json")
	c := config.Config{
		Version: 1, Coordinator: "http://127.0.0.1:1", Name: "M2 mini", Listen: "127.0.0.1:8788",
		Development: true, Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300, HostID: hostID,
	}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBundleReceiveRequiresEnrolledHost(t *testing.T) {
	path := bundleTestConfig(t, "")
	err := bundleCommand(context.Background(), "bundle-receive", []string{"--from-host", bundleTestID, "--config", path})
	if err == nil || !strings.Contains(err.Error(), "enroll this Mac") {
		t.Fatalf("unenrolled host not refused before credential access: %v", err)
	}
}

func TestBundleReceiveRejectsSelfAsDonor(t *testing.T) {
	path := bundleTestConfig(t, bundleTestID)
	err := bundleCommand(context.Background(), "bundle-receive", []string{"--from-host", bundleTestID, "--config", path})
	if err == nil || !strings.Contains(err.Error(), "must be different enrolled hosts") {
		t.Fatalf("self-transfer not refused: %v", err)
	}
}

func TestPeerNamesCommandRefusesUnenrolledHostBeforeCredentialAccess(t *testing.T) {
	path := bundleTestConfig(t, "")
	err := peerNamesCommand(context.Background(), []string{"--config", path, "--action", "list"})
	var ce *codedError
	if !errors.As(err, &ce) || ce.code != "not_enrolled" {
		t.Fatalf("expected not_enrolled before credential access, got %v", err)
	}
}

func TestBundleSendRequiresEnrolledHost(t *testing.T) {
	path := bundleTestConfig(t, "")
	err := bundleCommand(context.Background(), "bundle-send", []string{
		"--transfer", bundleTestID, "--bundle", "/tmp/b",
		"--receiver-key-sha256", strings.Repeat("a", 64), "--config", path,
	})
	if err == nil || !strings.Contains(err.Error(), "enroll this Mac") {
		t.Fatalf("unenrolled sender not refused before credential access: %v", err)
	}
}

func TestBundleSendRefusesTransferThatIsNotPendingForThisDonor(t *testing.T) {
	donor := "22222222-2222-4222-8222-222222222222"
	for name, state := range map[string]string{"already ready": "ready", "expired": "expired"} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected %s %s: nothing may be uploaded", r.Method, r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(map[string]string{
					"id": bundleTestID, "donorHostId": donor, "receiverHostId": bundleTestID,
					"publicKey": "AAAA", "expiresAt": "2099-01-01T00:00:00Z", "state": state,
				})
			}))
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "private", "config.json")
			c := config.Config{
				Version: 1, Coordinator: srv.URL, Name: "M2 mini", Listen: "127.0.0.1:8788",
				Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20,
				ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300, HostID: donor,
			}
			if err := config.Save(path, c); err != nil {
				t.Fatal(err)
			}
			s, err := config.NewSecrets(path, c)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Put(context.Background(), "host", strings.Repeat("h", 40)); err != nil {
				t.Fatal(err)
			}
			err = bundleCommand(context.Background(), "bundle-send", []string{
				"--transfer", bundleTestID, "--bundle", t.TempDir(),
				"--receiver-key-sha256", strings.Repeat("a", 64), "--config", path,
			})
			if err == nil || !strings.Contains(err.Error(), "not pending for this donor") {
				t.Fatalf("non-pending transfer not refused: %v", err)
			}
		})
	}
}

func TestBundleReceiveRejectsMismatchedTransferResponse(t *testing.T) {
	receiver := "22222222-2222-4222-8222-222222222222"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s: receiver must not poll a rejected transfer", r.Method, r.URL.Path)
		}
		// The coordinator answers with a transfer whose public key is not the one the receiver sent.
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id": bundleTestID, "donorHostId": bundleTestID, "receiverHostId": receiver,
			"publicKey": "AAAA", "expiresAt": "2099-01-01T00:00:00Z", "state": "pending",
		})
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "private", "config.json")
	c := config.Config{
		Version: 1, Coordinator: srv.URL, Name: "M2 mini", Listen: "127.0.0.1:8788",
		Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300, HostID: receiver,
	}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	s, err := config.NewSecrets(path, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(context.Background(), "host", strings.Repeat("h", 40)); err != nil {
		t.Fatal(err)
	}
	err = bundleCommand(context.Background(), "bundle-receive", []string{"--from-host", bundleTestID, "--config", path})
	if err == nil || !strings.Contains(err.Error(), "unexpected transfer response") {
		t.Fatalf("mismatched transfer response not refused: %v", err)
	}
}

func TestBundleReceivePollRejectsUnavailableOrChangedTransfer(t *testing.T) {
	receiver := "22222222-2222-4222-8222-222222222222"
	cases := []struct {
		name  string
		poll  func(created map[string]string) map[string]string
		wants string
	}{
		{"expired", func(c map[string]string) map[string]string { c["state"] = "expired"; return c }, "no longer available"},
		{"identity changed", func(c map[string]string) map[string]string {
			c["state"] = "ready"
			c["donorHostId"] = "33333333-3333-4333-8333-333333333333"
			return c
		}, "transfer identity changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var created map[string]string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					var req map[string]string
					_ = json.NewDecoder(r.Body).Decode(&req)
					created = map[string]string{
						"id": bundleTestID, "donorHostId": req["donorHostId"], "receiverHostId": receiver,
						"publicKey": req["publicKey"], "expiresAt": "2099-01-01T00:00:00Z", "state": "pending",
					}
					_ = json.NewEncoder(w).Encode(created)
					return
				}
				next := map[string]string{}
				for k, v := range created {
					next[k] = v
				}
				_ = json.NewEncoder(w).Encode(tc.poll(next))
			}))
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "private", "config.json")
			c := config.Config{
				Version: 1, Coordinator: srv.URL, Name: "M2 mini", Listen: "127.0.0.1:8788",
				Development: true, DevSecrets: true, Paused: true, MemoryLimitBytes: 256 << 20,
				ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300, HostID: receiver,
			}
			if err := config.Save(path, c); err != nil {
				t.Fatal(err)
			}
			s, err := config.NewSecrets(path, c)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Put(context.Background(), "host", strings.Repeat("h", 40)); err != nil {
				t.Fatal(err)
			}
			err = bundleCommand(context.Background(), "bundle-receive", []string{"--from-host", bundleTestID, "--config", path})
			if err == nil || !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("want %q, got %v", tc.wants, err)
			}
		})
	}
}
