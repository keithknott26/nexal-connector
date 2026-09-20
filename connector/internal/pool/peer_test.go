package pool

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func startTestPeer(t *testing.T, store *Store, identity Identity, registry *Registry, allowed []string) string {
	t.Helper()
	server, err := NewPeerServer(PeerOptions{Identity: identity, Registry: registry, AllowedPeers: allowed, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("peer serve: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("peer server did not stop")
		}
	})
	return "https://" + listener.Addr().String()
}

func testPeerClient(t *testing.T, id Identity, registry *Registry, remote string, endpoint string) *PeerClient {
	t.Helper()
	client, err := NewPeerClient(PeerClientOptions{Identity: id, Registry: registry,
		ExpectedDeviceID: remote, Endpoint: endpoint, MaxObjectBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func TestAuthenticatedPeerTransferProtectedReceiptAndDownload(t *testing.T) {
	registry := NewRegistry(nil)
	a := enrolledIdentity(t, registry)
	b := enrolledIdentity(t, registry)
	source := testStore(t, 1<<20, nil)
	replica := testStore(t, 1<<20, nil)
	endpoint := startTestPeer(t, replica, b, registry, []string{DeviceID(a.PublicKey)})
	client := testPeerClient(t, a, registry, DeviceID(b.PublicKey), endpoint)
	data := []byte("actually transferred through mutually authenticated TLS")
	blob, err := source.PutBytes(Protected, data)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := ReplicateProtected(context.Background(), source, a, registry, blob.Digest, []*PeerClient{client}, time.Now())
	if err != nil || len(receipts) != 2 || receipts[0].DeviceID == receipts[1].DeviceID {
		t.Fatalf("replication did not confirm distinct machines: %+v %v", receipts, err)
	}
	got, err := replica.Read(Protected, blob.Digest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("remote bytes %q %v", got, err)
	}
	rec, err := source.Publish(PublishRequest{Key: "real-network-copy", Blob: blob, Owner: "owner", Replicas: receipts},
		registry, time.Now(), time.Hour)
	if err != nil || rec.Manifest.StateAtCommit != ProtectedState {
		t.Fatalf("real confirmations did not publish: %+v %v", rec, err)
	}
	restore := testStore(t, 1<<20, nil)
	if err := client.Download(context.Background(), restore, blob); err != nil {
		t.Fatal(err)
	}
	if err := restore.Check(Protected, blob.Digest); err != nil {
		t.Fatal(err)
	}
	// Physical remote corruption cannot yield a success acknowledgment.
	if err := os.WriteFile(filepath.Join(replica.directory, "protected."+blob.Digest), []byte("damage"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Upload(context.Background(), blob, bytes.NewReader(data)); err == nil {
		t.Fatal("corrupt remote copy acknowledged")
	}
	if err := client.Download(context.Background(), testStore(t, 1<<20, nil), blob); err == nil {
		t.Fatal("corrupt remote bytes downloaded")
	}
}

func TestPeerAuthenticationPinsAndLiveRevocation(t *testing.T) {
	registry := NewRegistry(nil)
	a := enrolledIdentity(t, registry)
	b := enrolledIdentity(t, registry)
	c := enrolledIdentity(t, registry)
	store := testStore(t, 1<<20, nil)
	endpoint := startTestPeer(t, store, b, registry, []string{DeviceID(a.PublicKey)})
	blob := Blob{digestOf([]byte("x")), 1, Protected}
	// Being enrolled elsewhere is not being explicitly trusted by this server.
	untrusted := testPeerClient(t, c, registry, DeviceID(b.PublicKey), endpoint)
	if _, err := untrusted.Upload(context.Background(), blob, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("non-allowlisted client accepted")
	}
	wrongPin := testPeerClient(t, a, registry, DeviceID(c.PublicKey), endpoint)
	if _, err := wrongPin.Upload(context.Background(), blob, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("server certificate pin not enforced")
	}
	anonymous := &http.Client{Timeout: time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13},
	}}
	defer anonymous.CloseIdleConnections()
	if resp, err := anonymous.Get(endpoint + "/v1/objects/protected/" + blob.Digest); err == nil {
		resp.Body.Close()
		t.Fatal("anonymous TLS client accepted")
	}
	client := testPeerClient(t, a, registry, DeviceID(b.PublicKey), endpoint)
	if _, err := client.Upload(context.Background(), blob, bytes.NewReader([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if err := registry.Revoke(DeviceID(a.PublicKey)); err != nil {
		t.Fatal(err)
	}
	// Bypass the client's proactive check to exercise server revocation on a
	// reused TLS connection, not just the handshake callback.
	resp, err := client.client.Get(endpoint + "/v1/objects/protected/" + blob.Digest)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("revoked keepalive connection accepted: %d", resp.StatusCode)
		}
	}
	if _, err := client.Upload(context.Background(), blob, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked client not blocked: %v", err)
	}
}

func TestPeerBoundsTraversalAndNoGlobalListener(t *testing.T) {
	registry := NewRegistry(nil)
	a := enrolledIdentity(t, registry)
	b := enrolledIdentity(t, registry)
	store := testStore(t, 4, nil)
	server, err := NewPeerServer(PeerOptions{Identity: b, Registry: registry,
		AllowedPeers: []string{DeviceID(a.PublicKey)}, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	wildcard, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wildcard.Close()
	if err := server.Serve(wildcard); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("global listener permitted: %v", err)
	}
	endpoint := startTestPeer(t, store, b, registry, []string{DeviceID(a.PublicKey)})
	client := testPeerClient(t, a, registry, DeviceID(b.PublicKey), endpoint)
	blob := Blob{digestOf([]byte("12345")), 5, Protected}
	if _, err := client.Upload(context.Background(), blob, bytes.NewReader([]byte("12345"))); err == nil {
		t.Fatal("remote quota not enforced")
	}
	if used, _ := store.Usage(); used != 0 {
		t.Fatal("failed upload consumed quota")
	}
	for _, path := range []string{"/v1/objects/protected/../../secret", "/v1/objects/cache/%2e%2e", "/v1/objects/cache/abc?path=x"} {
		resp, err := client.client.Get(endpoint + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatalf("unsafe route accepted: %s", path)
		}
	}
	req, _ := http.NewRequest(http.MethodPut, endpoint+"/v1/objects/protected/"+blob.Digest, nil)
	req.ContentLength = 2 << 20
	resp, err := client.client.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatal("oversized request accepted")
		}
	}
	for _, endpoint := range []string{"http://127.0.0.1:1234", "https://0.0.0.0:1234",
		"https://example.com:443", "https://8.8.8.8:443", "https://127.0.0.1:1234/other"} {
		if _, err := NewPeerClient(PeerClientOptions{Identity: a, Registry: registry,
			ExpectedDeviceID: DeviceID(b.PublicKey), Endpoint: endpoint, MaxObjectBytes: 100}); err == nil {
			t.Fatalf("unsafe endpoint accepted %s", endpoint)
		}
	}
}

func TestPinnedTLSRejectsFakeSuccessReceipt(t *testing.T) {
	registry := NewRegistry(nil)
	a := enrolledIdentity(t, registry)
	b := enrolledIdentity(t, registry)
	policy, err := newPeerPolicy(registry, []string{DeviceID(a.PublicKey)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := peerTLS(b, policy, true)
	if err != nil {
		t.Fatal(err)
	}
	// peerTLS mints on demand and leaves Certificates empty; httptest fills an
	// empty Certificates with its own generated certificate, and Go skips
	// GetCertificate when Certificates is non-empty and no SNI is sent. Pin b's
	// real device certificate so this stays a test about a genuinely pinned peer
	// returning an unsigned receipt, not about an unrelated certificate.
	device, err := peerCertificate(b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Certificates = []tls.Certificate{device}
	blob := Blob{digestOf([]byte("x")), 1, Protected}
	fake := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		// A valid TLS peer's unsigned JSON assertion is still insufficient.
		_ = json.NewEncoder(w).Encode(peerAcknowledgment{Blob: blob, Receipt: ReplicaReceipt{
			DeviceID: DeviceID(b.PublicKey), Digest: blob.Digest, Size: 1, Class: Protected}})
	}))
	fake.TLS = cfg
	fake.StartTLS()
	defer fake.Close()
	client := testPeerClient(t, a, registry, DeviceID(b.PublicKey), fake.URL)
	if _, err := client.Upload(context.Background(), blob, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("HTTP 201 without signed durable receipt accepted")
	}
	source := testStore(t, 100, nil)
	source.PutBytes(Protected, []byte("x"))
	receipts, err := ReplicateProtected(context.Background(), source, a, registry, blob.Digest, nil, time.Now())
	if !errors.Is(err, ErrDurability) || len(receipts) != 1 {
		t.Fatalf("one local copy called durable: %+v %v", receipts, err)
	}
}

// A peer certificate lives 24 hours and used to be minted exactly once, so any
// host running longer than that presented a certificate its own peers had to
// reject. Both ends must re-mint as expiry approaches.
func TestPeerCertificatesRotateBeforeExpiry(t *testing.T) {
	var clockMu sync.Mutex
	current := time.Now()
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return current
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		current = current.Add(d)
		clockMu.Unlock()
	}
	registry := NewRegistry(nil)
	a := enrolledIdentity(t, registry)
	b := enrolledIdentity(t, registry)
	source := &peerCertificates{identity: b, now: clock}
	first, err := source.certificate()
	if err != nil {
		t.Fatal(err)
	}
	again, err := source.certificate()
	if err != nil {
		t.Fatal(err)
	}
	if first.Leaf.SerialNumber.Cmp(again.Leaf.SerialNumber) != 0 {
		t.Fatal("certificate re-minted while still comfortably valid")
	}
	// Inside the renewal margin, before the certificate is actually invalid.
	advance(peerCertificateLifetime - peerCertificateRenewal/2)
	renewed, err := source.certificate()
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) == 0 {
		t.Fatal("certificate not renewed as expiry approached")
	}
	if !renewed.Leaf.NotAfter.After(first.Leaf.NotAfter) {
		t.Fatal("renewal did not extend validity")
	}
	// A clock that jumped backwards leaves the cached certificate not yet valid.
	advance(-48 * time.Hour)
	rewound, err := source.certificate()
	if err != nil {
		t.Fatal(err)
	}
	if rewound.Leaf.SerialNumber.Cmp(renewed.Leaf.SerialNumber) == 0 {
		t.Fatal("cached certificate reused after the clock moved backwards")
	}
	if _, err := (&peerCertificates{identity: Identity{}, now: clock}).certificate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid identity minted a certificate: %v", err)
	}
	// End to end: a server and a client built now must still transfer after both
	// of their certificates would have expired.
	advance(48 * time.Hour)
	store := testStore(t, 1<<20, nil)
	server, err := NewPeerServer(PeerOptions{Identity: b, Registry: registry,
		AllowedPeers: []string{DeviceID(a.PublicKey)}, Store: store, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})
	client, err := NewPeerClient(PeerClientOptions{Identity: a, Registry: registry,
		ExpectedDeviceID: DeviceID(b.PublicKey), Endpoint: "https://" + listener.Addr().String(),
		MaxObjectBytes: 1 << 20, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	fresh := []byte("transferred on a freshly minted certificate")
	if _, err := client.Upload(context.Background(), Blob{digestOf(fresh), int64(len(fresh)), Protected},
		bytes.NewReader(fresh)); err != nil {
		t.Fatal(err)
	}
	advance(peerCertificateLifetime + time.Hour)
	// Force a new handshake instead of reusing the established connection.
	client.transport.CloseIdleConnections()
	aged := []byte("transferred after both certificates would have expired")
	if _, err := client.Upload(context.Background(), Blob{digestOf(aged), int64(len(aged)), Protected},
		bytes.NewReader(aged)); err != nil {
		t.Fatalf("expired peer certificate was never rotated: %v", err)
	}
}

// postQuantumKeyExchange reports whether a negotiated group is one of Go's hybrid
// ML-KEM key exchanges. This observes our own handshake; it is a self-report about
// this process, not attestation of the peer's configuration.
func postQuantumKeyExchange(id tls.CurveID) bool {
	switch id { // gitleaks:allow -- public algorithm names, not key material
	case tls.X25519MLKEM768, tls.SecP256r1MLKEM768, tls.SecP384r1MLKEM1024:
		return true
	}
	return false
}

// Peer mTLS must negotiate a post-quantum group. Go offers the hybrid ML-KEM
// groups only while CurvePreferences is nil, and the GODEBUG that gates them comes
// from go.mod's go directive, so both "pinning" a curve and lowering that directive
// would downgrade this handshake to classical X25519 with no other visible effect.
// Both ends run this code, so the assertion covers the real server and client
// configurations rather than a synthetic pair.
func TestPeerHandshakeUsesPostQuantumKeyExchange(t *testing.T) {
	registry := NewRegistry(nil)
	a := enrolledIdentity(t, registry)
	b := enrolledIdentity(t, registry)
	policy, err := newPeerPolicy(registry, []string{DeviceID(a.PublicKey)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range []bool{true, false} {
		cfg, err := peerTLS(b, policy, server)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.CurvePreferences != nil {
			t.Fatalf("server=%v: CurvePreferences is set, so Go offers only the listed groups and post-quantum key agreement is silently dropped", server)
		}
		if cfg.MinVersion != tls.VersionTLS13 {
			t.Fatalf("server=%v: TLS floor is %#x, and the ML-KEM groups exist only in TLS 1.3", server, cfg.MinVersion)
		}
	}
	store := testStore(t, 100, nil)
	endpoint := startTestPeer(t, store, b, registry, []string{DeviceID(a.PublicKey)})
	client := testPeerClient(t, a, registry, DeviceID(b.PublicKey), endpoint)
	// Any authenticated route completes a full mutual handshake; the response
	// status is irrelevant, the negotiated group is the subject.
	resp, err := client.client.Get(endpoint + "/v1/objects/protected/" + digestOf([]byte("absent")))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.TLS == nil {
		t.Fatal("peer transfer completed without TLS")
	}
	if resp.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("negotiated TLS version %#x", resp.TLS.Version)
	}
	if !postQuantumKeyExchange(resp.TLS.CurveID) {
		t.Fatalf("classical key exchange negotiated between peers: %v (%d)", resp.TLS.CurveID, resp.TLS.CurveID)
	}
	t.Logf("negotiated %v (%d) at TLS %#x", resp.TLS.CurveID, resp.TLS.CurveID, resp.TLS.Version)
}
