package tunnel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/config"
)

func fixture(t *testing.T) config.Tunnel {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "cloudflared")
	b := []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'cloudflared version 2026.1.0 (test)'; exit 0; fi\nprintf '%s\\n' '{\"message\":\"Registered tunnel connection\",\"protocol\":\"http2\"}'\nsleep 10\n")
	if err := os.WriteFile(binary, b, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	token := filepath.Join(dir, "tunnel.token")
	if err := os.WriteFile(token, []byte(strings.Repeat("t", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	return config.Tunnel{Binary: binary, SHA256: hex.EncodeToString(sum[:]), Version: "2026.1.0", Architecture: runtime.GOARCH,
		SourceURL:          "https://github.com/cloudflare/cloudflared/releases/download/2026.1.0/cloudflared-darwin-arm64.tgz",
		VerificationMethod: "TEST FIXTURE ONLY; not publisher verified", TokenFile: token, Hostname: "host.example.test"}
}
func TestStrictLaunchFlagsNoCommandlineCredential(t *testing.T) {
	got, err := BuildArgs("/private/effective.json", "/private/token")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tunnel", "--config", "/private/effective.json", "--no-autoupdate", "--protocol", "quic", "--logformat", "json", "run", "--post-quantum", "--token-file", "/private/token"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	for _, arg := range got {
		if arg == "--token" || arg == "http2" || arg == "--no-tls-verify" {
			t.Fatal("unsafe argument")
		}
	}
	t.Setenv("TUNNEL_TRANSPORT_PROTOCOL", "http2")
	t.Setenv("TUNNEL_POST_QUANTUM", "false")
	t.Setenv("TUNNEL_TOKEN", "do-not-leak")
	for _, env := range CleanEnv() {
		if strings.Contains(env, "TUNNEL_") || strings.Contains(env, "do-not-leak") {
			t.Fatal("ambient policy override leaked")
		}
	}
	if _, err := BuildArgs("relative", "/private/token"); err == nil {
		t.Fatal("relative config accepted")
	}
}
func TestPinConfigAndVersion(t *testing.T) {
	pin := fixture(t)
	e, err := Check(context.Background(), pin, "127.0.0.1:8788")
	if err != nil || !e.Configured || e.Verified || e.Attestation {
		t.Fatalf("%+v %v", e, err)
	}
	pin.SHA256 = strings.Repeat("0", 64)
	if Validate(pin, "127.0.0.1:8788") == nil {
		t.Fatal("wrong digest accepted")
	}
	pin = fixture(t)
	pin.Binary = "cloudflared"
	if Validate(pin, "127.0.0.1:8788") == nil {
		t.Fatal("PATH lookup allowed")
	}
	pin = fixture(t)
	pin.SourceURL = "https://evil.test/cloudflared"
	if Validate(pin, "127.0.0.1:8788") == nil {
		t.Fatal("unofficial source accepted")
	}
	pin = fixture(t)
	_ = os.Chmod(pin.TokenFile, 0644)
	if Validate(pin, "127.0.0.1:8788") == nil {
		t.Fatal("public token file accepted")
	}
}
func TestQUICIsNotPQAttestation(t *testing.T) {
	e := Observe(Evidence{Configured: true}, []byte(`{"message":"Registered tunnel connection","protocol":"quic"}`), time.Now())
	if !e.Connected || e.Verified || e.Attestation || e.ObservedKeyAgreement != "" {
		t.Fatalf("%+v", e)
	}
	e = Observe(e, []byte(`{"message":"handshake","keyAgreement":"X25519MLKEM768"}`), time.Now()) // gitleaks:allow -- public algorithm name, not a key
	if e.ObservedKeyAgreement == "" || e.Verified || e.Attestation {
		t.Fatal("diagnostic turned into attestation")
	}
}
func TestNoDowngradeAndQuarantineSticky(t *testing.T) {
	for _, line := range []string{
		`{"message":"Registered tunnel connection","protocol":"http2"}`,
		`{"message":"Switching to http2"}`,
		`{"message":"handshake","curve":"X25519"}`,
		`{"message":"post-quantum disabled"}`,
	} {
		e := Observe(Evidence{Configured: true, Connected: true}, []byte(line), time.Now())
		if !e.Quarantined || e.Connected {
			t.Fatalf("downgrade accepted: %+v", e)
		}
		e = Observe(e, []byte(`{"message":"Registered tunnel connection","protocol":"quic"}`), time.Now())
		if !e.Quarantined || e.Connected {
			t.Fatal("quarantine cleared by reconnect")
		}
	}
}
func TestEffectiveConfigCatchall(t *testing.T) {
	pin := fixture(t)
	path := filepath.Join(t.TempDir(), "private", "effective.json")
	if err := writeEffectiveConfig(path, pin, "127.0.0.1:8788"); err != nil {
		t.Fatal(err)
	}
	b, err := config.ReadPrivate(path, 8192)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, required := range []string{`"post-quantum": true`, `"protocol": "quic"`, `http_status:404`, `http://127.0.0.1:8788`} {
		if !strings.Contains(s, required) {
			t.Fatal("missing strict effective config")
		}
	}
	if strings.Contains(s, strings.Repeat("t", 64)) {
		t.Fatal("token leaked to effective config")
	}
}
