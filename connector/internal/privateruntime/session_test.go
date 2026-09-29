package privateruntime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func envelope(t *testing.T, files []File) Envelope {
	t.Helper()
	public, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(Manifest{SchemaVersion: 1, ReleaseID: "release_1", Entrypoint: "runtime", Files: files})
	return Envelope{base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw)), base64.StdEncoding.EncodeToString(public)}
}
func file(data []byte) File {
	h := sha256.Sum256(data)
	return File{"runtime", int64(len(data)), hex.EncodeToString(h[:]), "private-inference/release/runtime"}
}

type fake struct {
	session   Session
	data      []byte
	downloads int
	closed    int
	opens     int
}

func (f *fake) OpenRuntime(context.Context, string) (Session, error) {
	f.opens++
	f.session.ServerNow = time.Now()
	f.session.ExpiresAt = f.session.ServerNow.Add(30 * time.Second)
	return f.session, nil
}
func (f *fake) RenewRuntime(context.Context, string, string) (Lease, error) {
	now := time.Now()
	return Lease{1, now, now.Add(30 * time.Second)}, nil
}
func (f *fake) DownloadRuntime(ctx context.Context, _, _ string, _ int, w io.Writer, _ int64) error {
	f.downloads++
	_, e := w.Write(f.data)
	return e
}
func (f *fake) CloseRuntime(context.Context, string, string) error { f.closed++; return nil }
func fixture(t *testing.T) (*fake, Manager) {
	t.Helper()
	data := []byte("not executed by tests")
	f := &fake{session: Session{Lease: Lease{SchemaVersion: 1}, SessionID: "session_1", NetworkID: "network_1", Bundle: envelope(t, []File{file(data)})}, data: data}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	m := Manager{API: f, HostID: "host_1", Root: root, Poll: time.Millisecond, Connected: func() bool { return true }}
	return f, m
}
func TestSignatureAndPathBoundary(t *testing.T) {
	good := file([]byte("hello"))
	e := envelope(t, []File{good})
	if _, err := Verify(e); err != nil {
		t.Fatal(err)
	}
	e.Payload = base64.StdEncoding.EncodeToString([]byte("tampered"))
	if _, err := Verify(e); err == nil {
		t.Fatal("bad signature accepted")
	}
	for _, name := range []string{"../outside", "/absolute", "a/../runtime", "session.json", "a//b"} {
		f := good
		f.Path = name
		if _, err := Verify(envelope(t, []File{good, f})); err == nil {
			t.Fatalf("unsafe path %s accepted", name)
		}
	}
	if _, err := Verify(envelope(t, []File{good, good})); err == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestEveryRunRedownloadsAndRemovesPrivateFiles(t *testing.T) {
	f, m := fixture(t)
	runs := 0
	m.Run = func(ctx context.Context, dir, lease string) error {
		runs++
		if _, e := os.Stat(filepath.Join(dir, "runtime")); e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(lease)
		if e != nil || !json.Valid(b) {
			t.Fatal("missing lease")
		}
		return nil
	}
	for i := 0; i < 2; i++ {
		if err := m.once(context.Background()); err != nil {
			t.Fatal(err)
		}
		entries, _ := os.ReadDir(m.Root)
		if len(entries) != 0 {
			t.Fatal("private cache survived")
		}
	}
	if runs != 2 || f.downloads != 2 || f.opens != 2 || f.closed != 2 {
		t.Fatal("execution reused a cached runtime")
	}
}
func TestDisconnectCancelsAndCleans(t *testing.T) {
	f, m := fixture(t)
	var online atomic.Bool
	online.Store(true)
	m.Connected = online.Load
	started := make(chan struct{})
	m.Run = func(ctx context.Context, _, _ string) error { close(started); <-ctx.Done(); return ctx.Err() }
	done := make(chan error, 1)
	go func() { done <- m.once(context.Background()) }()
	<-started
	online.Store(false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not stop runtime")
	}
	entries, _ := os.ReadDir(m.Root)
	if len(entries) != 0 || f.closed != 1 {
		t.Fatal("session not cleaned")
	}
}
func TestBadDownloadNeverExecutes(t *testing.T) {
	f, m := fixture(t)
	f.data = []byte("corrupt")
	ran := false
	m.Run = func(context.Context, string, string) error { ran = true; return nil }
	if m.once(context.Background()) == nil || ran {
		t.Fatal("corrupt artifact executed")
	}
	entries, _ := os.ReadDir(m.Root)
	if len(entries) != 0 {
		t.Fatal("failed download retained")
	}
}
func TestDisconnectedDefaultNeverDownloads(t *testing.T) {
	f, m := fixture(t)
	m.Connected = func() bool { return false }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.RunLoop(ctx); err != nil {
		t.Fatal(err)
	}
	if f.opens != 0 || f.downloads != 0 {
		t.Fatal("offline execution")
	}
}
func TestExpiredOrOverlongLeaseRejected(t *testing.T) {
	for _, duration := range []time.Duration{-time.Second, 0, 31 * time.Second} {
		now := time.Now()
		if _, err := leaseDuration(Lease{1, now, now.Add(duration)}); err == nil {
			t.Fatal("invalid lease accepted")
		}
	}
}

func TestSignedNetworkScopeCompatibility(t *testing.T) {
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"", "network", "global"} {
		raw, _ := json.Marshal(Manifest{AssignmentScope: scope, SchemaVersion: 1, ReleaseID: "release", Entrypoint: "runtime", Files: []File{file([]byte("fixture"))}})
		e := Envelope{base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw)), base64.StdEncoding.EncodeToString(public)}
		_, err := Verify(e)
		if (err != nil) != (scope == "global") {
			t.Fatalf("unexpected scope acceptance: %s", scope)
		}
	}
}

func networkEnvelope(t *testing.T) Envelope {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Manifest{AssignmentScope: "network", SchemaVersion: 1, ReleaseID: "release_1", Entrypoint: "runtime", Files: []File{file([]byte("not executed by tests"))}})
	return Envelope{base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw)), base64.StdEncoding.EncodeToString(public)}
}

func TestNetworkRuntimeRequiresLocalAdmission(t *testing.T) {
	for _, mode := range []string{"missing", "denied", "invalid", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f, m := fixture(t)
			f.session.Bundle = networkEnvelope(t)
			released := false
			if mode != "missing" {
				m.AdmitNetwork = func(ctx context.Context, _ Manifest) (context.Context, func(), error) {
					release := func() { released = true }
					if mode == "denied" {
						return nil, release, ErrClosed
					}
					if mode == "invalid" {
						return nil, release, nil
					}
					stopped, cancel := context.WithCancel(ctx)
					cancel()
					return stopped, release, nil
				}
			}
			m.Run = func(context.Context, string, string) error { t.Fatal("unadmitted runtime executed"); return nil }
			if m.once(context.Background()) == nil || f.downloads != 0 || f.closed != 1 {
				t.Fatal("unadmitted runtime was not rejected before download")
			}
			if mode != "missing" && !released {
				t.Fatal("failed admission leaked reservation")
			}
		})
	}
}

func TestNetworkReservationRevocationStopsRuntimeBeforeRelease(t *testing.T) {
	f, m := fixture(t)
	f.session.Bundle = networkEnvelope(t)
	admission, revoke := context.WithCancel(context.Background())
	defer revoke()
	started := make(chan struct{})
	stopped := false
	released := false
	m.AdmitNetwork = func(context.Context, Manifest) (context.Context, func(), error) {
		return admission, func() {
			if !stopped {
				t.Error("reservation released before child stopped")
			}
			entries, _ := os.ReadDir(m.Root)
			if len(entries) != 0 {
				t.Error("reservation released before payload cleanup")
			}
			released = true
		}, nil
	}
	m.Run = func(ctx context.Context, _, _ string) error {
		close(started)
		<-ctx.Done()
		stopped = true
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- m.once(context.Background()) }()
	<-started
	revoke()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reservation revocation did not stop runtime")
	}
	if !released || f.closed != 1 {
		t.Fatal("reservation or session leaked")
	}
}
