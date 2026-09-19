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
