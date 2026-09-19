package pager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func setup(t *testing.T, n int) (*Client, *Store, context.CancelFunc) {
	t.Helper()
	k, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	st, err := k.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	ct, err := k.ClientTLS()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(n)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln, st) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("server failed to stop")
		}
	})
	cl, err := Dial(ctx, ln.Addr().String(), ct)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl, s, cancel
}
func TestPortableTCP(t *testing.T) {
	for _, slots := range []int{1, 4, 16} {
		t.Run(string(rune('A'+slots)), func(t *testing.T) {
			c, _, _ := setup(t, 32)
			r, err := Portable(context.Background(), c, slots)
			if err != nil {
				t.Fatal(err)
			}
			if !r.Verified || r.NativeHVFExecuted || r.VerifiedBytes != 32*PageSize ||
				r.Cache.PeakResidentPages != slots || r.Cache.Evictions != uint64(2*(32-slots)) ||
				r.Transport.Puts != 32 || r.Transport.Gets != 64 {
				t.Fatalf("bad report: %+v", r)
			}
		})
	}
}
func TestQuota(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 257} {
		if _, err := NewStore(n); err == nil {
			t.Fatalf("accepted %d", n)
		}
	}
}
func TestPrivateAddress(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:1", "[::]:2", "example.com:3", "8.8.8.8:4", "garbage"} {
		if PrivateAddress(addr) == nil {
			t.Fatal(addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "192.168.1.2:123", "10.1.1.2:3", "[::1]:2", "[fd00::1]:2"} {
		if err := PrivateAddress(addr); err != nil {
			t.Fatal(addr, err)
		}
	}
}
func TestBounds(t *testing.T) {
	c, _, _ := setup(t, 4)
	for _, n := range []int{-1, 4, 99} {
		if _, err := c.Get(context.Background(), n); err == nil {
			t.Fatal(n)
		}
	}
	if err := c.Put(context.Background(), 0, []byte{1}); err == nil {
		t.Fatal("short write")
	}
	for _, n := range []int{0, 4, 17} {
		if _, err := NewCache(c, n); err == nil {
			t.Fatal(n)
		}
	}
}
func TestVersionsAndCorruption(t *testing.T) {
	for _, kind := range []string{"stale", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			c, s, _ := setup(t, 4)
			ctx := context.Background()
			if err := c.Put(ctx, 1, Pattern(1)); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			if kind == "stale" {
				s.versions[1]++
			} else {
				s.data[1][8] ^= 1
			}
			s.mu.Unlock()
			if _, err := c.Get(ctx, 1); err == nil {
				t.Fatal("accepted corrupt/stale")
			}
			if _, err := c.Get(ctx, 0); err == nil {
				t.Fatal("failed session continued")
			}
		})
	}
}
func TestCancelAndDonorLoss(t *testing.T) {
	c, _, cancel := setup(t, 4)
	cancel()
	ctx, end := context.WithTimeout(context.Background(), time.Second)
	defer end()
	if _, err := c.Get(ctx, 0); err == nil {
		t.Fatal("read succeeded after donor loss")
	}
	if _, err := c.Get(ctx, 0); err == nil {
		t.Fatal("session continued")
	}
}
func TestConcurrentClientSerializes(t *testing.T) {
	c, _, _ := setup(t, 16)
	var wg sync.WaitGroup
	for p := 0; p < 16; p++ {
		wg.Add(1)
		go func(page int) {
			defer wg.Done()
			ctx := context.Background()
			if err := c.Put(ctx, page, Pattern(page)); err != nil {
				t.Error(err)
				return
			}
			got, err := c.Get(ctx, page)
			if err != nil || !bytes.Equal(got, Pattern(page)) {
				t.Error("concurrent roundtrip failed", err)
			}
		}(p)
	}
	wg.Wait()
}
func TestAuthenticationAndNoDowngrade(t *testing.T) {
	for _, mode := range []string{"missing-client-cert", "wrong-CA", "no-hybrid"} {
		t.Run(mode, func(t *testing.T) {
			k, _ := NewKeys()
			st, _ := k.ServerTLS()
			ct, _ := k.ClientTLS()
			if mode == "missing-client-cert" {
				ct.Certificates = nil
			}
			if mode == "wrong-CA" {
				other, _ := NewKeys()
				ct, _ = other.ClientTLS()
			}
			if mode == "no-hybrid" {
				ct.CurvePreferences = []tls.CurveID{tls.X25519}
			}
			s, _ := NewStore(4)
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- s.Serve(ctx, ln, st) }()
			defer func() { cancel(); <-done }()
			c, err := Dial(ctx, ln.Addr().String(), ct)
			if c != nil {
				c.Close()
			}
			if err == nil {
				t.Fatal("accepted insecure peer")
			}
		})
	}
}
func TestKeysPrivateAndNoOverwrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "keys")
	if err := InitKeys(root); err != nil {
		t.Fatal(err)
	}
	if err := InitKeys(root); err == nil {
		t.Fatal("overwrote keys")
	}
	for _, role := range []string{"donor", "client"} {
		if _, err := LoadTLS(filepath.Join(root, role), role == "donor"); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(root, "client", "key.pem")
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTLS(filepath.Join(root, "client"), false); err == nil {
		t.Fatal("accepted exposed key")
	}
}
func TestSymlinkKeyRejected(t *testing.T) {
	root := filepath.Join(t.TempDir(), "keys")
	if err := InitKeys(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "client", "key.pem")
	if err := os.Rename(path, path+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".real", path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTLS(filepath.Join(root, "client"), false); err == nil {
		t.Fatal("accepted symlink")
	}
}

type mockBackend struct {
	pages   map[int][]byte
	failPut bool
}

func (m *mockBackend) Pages() int { return 8 }
func (m *mockBackend) Get(_ context.Context, p int) ([]byte, error) {
	if v := m.pages[p]; v != nil {
		return append([]byte(nil), v...), nil
	}
	return make([]byte, PageSize), nil
}
func (m *mockBackend) Put(_ context.Context, p int, b []byte) error {
	if m.failPut {
		return errors.New("injected write failure")
	}
	m.pages[p] = append([]byte(nil), b...)
	return nil
}
func TestFailedEvictionRetainsDirtyData(t *testing.T) {
	b := &mockBackend{pages: make(map[int][]byte), failPut: true}
	c, _ := NewCache(b, 1)
	ctx := context.Background()
	if err := c.Write(ctx, 0, Pattern(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(ctx, 1); err == nil {
		t.Fatal("expected failed eviction")
	}
	got, err := c.Read(ctx, 0)
	if err != nil || !bytes.Equal(got, Pattern(0)) {
		t.Fatal("lost dirty page")
	}
	if c.Stats().PeakResidentPages != 1 {
		t.Fatal("cache grew")
	}
	b.failPut = false
	if err = c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.pages[0], Pattern(0)) {
		t.Fatal("flush lost data")
	}
}
func TestCacheDoesNotExposeMutableBacking(t *testing.T) {
	c, _ := NewCache(&mockBackend{pages: make(map[int][]byte)}, 1)
	ctx := context.Background()
	in := Pattern(0)
	if err := c.Write(ctx, 0, in); err != nil {
		t.Fatal(err)
	}
	in[0] ^= 1
	a, _ := c.Read(ctx, 0)
	a[0] ^= 1
	b, _ := c.Read(ctx, 0)
	if !bytes.Equal(b, Pattern(0)) {
		t.Fatal("mutable page escaped")
	}
}
func TestShortWriter(t *testing.T) {
	if err := writeFull(zeroWriter{}, []byte{1}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

// Craft replies over a pipe to verify payload hashing and ambiguous acknowledgement
// handling independently of the normal donor implementation.
func fakeClient() (*Client, net.Conn) {
	a, b := net.Pipe()
	zero := sha256.Sum256(make([]byte, PageSize))
	return &Client{c: a, pages: 2, versions: make([]uint64, 2), hashes: [][32]byte{zero, zero}}, b
}
func TestPayloadCorruption(t *testing.T) {
	c, peer := fakeClient()
	defer c.Close()
	go func() {
		defer peer.Close()
		h := make([]byte, 13)
		io.ReadFull(peer, h)
		resp := make([]byte, 41)
		copy(resp[9:], c.hashes[0][:])
		writeFull(peer, resp)
		body := make([]byte, PageSize)
		body[0] = 1
		writeFull(peer, body)
	}()
	if _, err := c.Get(context.Background(), 0); err == nil {
		t.Fatal("corrupt payload accepted")
	}
}
func TestLostWriteAcknowledgementPoisons(t *testing.T) {
	c, peer := fakeClient()
	defer c.Close()
	go func() { defer peer.Close(); io.CopyN(io.Discard, peer, 13+PageSize) }()
	if err := c.Put(context.Background(), 0, Pattern(0)); err == nil {
		t.Fatal("ambiguous write accepted")
	}
	if err := c.Put(context.Background(), 0, Pattern(0)); err == nil {
		t.Fatal("retried")
	}
}
func TestCancellationInterruptsBlockedRead(t *testing.T) {
	c, peer := fakeClient()
	defer c.Close()
	defer peer.Close()
	go func() { var h [13]byte; io.ReadFull(peer, h[:]) }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Get(ctx, 0); done <- err }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("no error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation hung")
	}
}
func TestBrokerRejectsBogusCompletion(t *testing.T) {
	c, _, _ := setup(t, 4)
	r := Report{CachePayloadLimitBytes: PageSize}
	var b bytes.Buffer
	b.WriteByte('D')
	b.Write(make([]byte, 32))
	if err := Broker(context.Background(), c, &b, io.Discard, &r); err == nil {
		t.Fatal("accepted fake native success")
	}
}
func TestBrokerUnknownAndOutOfRange(t *testing.T) {
	for _, op := range []byte{'X', 'G'} {
		c, _, _ := setup(t, 4)
		r := Report{CachePayloadLimitBytes: PageSize}
		var h [5]byte
		h[0] = op
		binary.BigEndian.PutUint32(h[1:], 500)
		if err := Broker(context.Background(), c, bytes.NewReader(h[:]), io.Discard, &r); err == nil {
			t.Fatal("accepted bad IPC")
		}
	}
}
func TestCancelledContextNoRequest(t *testing.T) {
	c, _, _ := setup(t, 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Get(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if c.Stats().Gets != 0 {
		t.Fatal("request happened")
	}
}

func TestBrokerCompleteRoundTrip(t *testing.T) {
	c, _, _ := setup(t, 4)
	// A test-only model of the child IPC. This tests the broker, not HVF.
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	report := Report{CachePayloadLimitBytes: PageSize}
	go func() { done <- Broker(context.Background(), c, a, a, &report) }()
	request := func(op byte, page int, data []byte) []byte {
		t.Helper()
		_ = b.SetDeadline(time.Now().Add(time.Second))
		var h [5]byte
		h[0] = op
		binary.BigEndian.PutUint32(h[1:], uint32(page))
		if err := writeFull(b, h[:]); err != nil {
			t.Fatal(err)
		}
		if op == 'P' {
			if err := writeFull(b, data); err != nil {
				t.Fatal(err)
			}
		}
		var status [1]byte
		if _, err := io.ReadFull(b, status[:]); err != nil || status[0] != 0 {
			t.Fatal("IPC status", err)
		}
		if op == 'G' {
			out := make([]byte, PageSize)
			if _, err := io.ReadFull(b, out); err != nil {
				t.Fatal(err)
			}
			return out
		}
		return nil
	}
	for p := 0; p < 4; p++ {
		request('G', p, nil)
		request('P', p, Pattern(p))
	}
	for p := 3; p >= 0; p-- {
		if !bytes.Equal(request('G', p, nil), Pattern(p)) {
			t.Fatal("IPC data mismatch")
		}
	}
	var packet [33]byte
	packet[0] = 'D'
	for i, value := range []uint64{8, 6, 4 * PageSize, 1} {
		binary.BigEndian.PutUint64(packet[1+i*8:], value)
	}
	if err := writeFull(b, packet[:]); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if report.VerifiedBytes != 4*PageSize || report.NativeHVFExecuted {
		t.Fatal("incorrect broker report")
	}
}

func TestSecondConnectionRejected(t *testing.T) {
	c, _, _ := setup(t, 4)
	conn, err := net.DialTimeout("tcp", c.c.RemoteAddr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err = conn.Read(b[:]); err == nil {
		t.Fatal("excess connection was not closed")
	}
}
