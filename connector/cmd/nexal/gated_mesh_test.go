package main

import (
	"path/filepath"
	"testing"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/mesh"
)

type stubMesh struct{ calls int }

func (s *stubMesh) Snapshot() mesh.Status {
	s.calls++
	return mesh.Status{ProviderAvailable: true, Lifecycle: mesh.LifecycleUnavailable, SelfTunnelAddress: "100.64.0.9"}
}

func TestEnrollmentGatedMeshHidesRuntimeUntilEnrolled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	inner := &stubMesh{}
	gated := enrollmentGatedMesh{path: path, inner: inner}

	// Missing config: not enrolled, the inner runtime must not be consulted.
	if gated.Enrolled() {
		t.Fatal("missing config reported as enrolled")
	}
	if got := gated.Snapshot(); got.ProviderAvailable || got.SelfTunnelAddress != "" || inner.calls != 0 {
		t.Fatalf("unenrolled snapshot leaked runtime: %+v calls=%d", got, inner.calls)
	}

	// Config without an enrollment: still not enrolled.
	if err := config.Save(path, validGatedConfig()); err != nil {
		t.Fatal(err)
	}
	if gated.Enrolled() || inner.calls != 0 {
		t.Fatal("config without enrollment reported as enrolled")
	}

	// Enrolled: the inner snapshot is passed through.
	cfg := validGatedConfig()
	cfg.Enrollment = &config.EnrollmentState{SchemaVersion: 2, Status: "provisioning"}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if !gated.Enrolled() {
		t.Fatal("enrolled config not reported as enrolled")
	}
	if got := gated.Snapshot(); !got.ProviderAvailable || got.SelfTunnelAddress != "100.64.0.9" || inner.calls != 1 {
		t.Fatalf("enrolled snapshot not delegated: %+v calls=%d", got, inner.calls)
	}

	// An expired guest lease hides the runtime again.
	now := time.Now().UTC()
	cfg.GuestAccess = &config.GuestAccess{GrantID: "grant-1", MeshHostname: "nexal-guest-grant-1", ReceivedAt: now.Format(time.RFC3339Nano), AccessExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), InviterEmail: "owner@example.com", Expired: true}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	if gated.Enrolled() {
		t.Fatal("expired guest access reported as enrolled")
	}
	if got := gated.Snapshot(); got.ProviderAvailable || inner.calls != 1 {
		t.Fatalf("expired guest snapshot leaked runtime: %+v calls=%d", got, inner.calls)
	}
}

func validGatedConfig() config.Config {
	return config.Config{Version: 1, Coordinator: "https://example.invalid", Name: "Test Mac", Listen: "127.0.0.1:8788", Development: true, DevSecrets: true, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 300}
}
