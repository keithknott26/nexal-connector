// Package pager is an isolated, deliberately bounded research protocol.
// It is not used by the production connector.
package pager

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	PageSize = 16384
	MaxPages = 256 // At most 4 MiB of donor payload, plus bounded protocol/runtime overhead.
	Timeout  = 3 * time.Second
)

// Store allocates the entire bounded donor payload upfront. It is volatile.
// A single live connection owns the store. There is no reconnect or failover.
type Store struct {
	mu       sync.Mutex
	data     [][]byte
	versions []uint64
	epoch    [16]byte
	active   chan struct{}
}

func NewStore(pages int) (*Store, error) {
	if pages < 2 || pages > MaxPages {
		return nil, errors.New("pages must be 2..256")
	}
	s := &Store{data: make([][]byte, pages), versions: make([]uint64, pages), active: make(chan struct{}, 1)}
	if _, err := rand.Read(s.epoch[:]); err != nil {
		return nil, err
	}
	for i := range s.data {
		s.data[i] = make([]byte, PageSize)
	}
	return s, nil
}

// Serve closes its listener and accepted sockets on cancellation. Excess clients
// are rejected before TLS handshake, keeping connection-related memory bounded.
func (s *Store) Serve(ctx context.Context, ln net.Listener, conf *tls.Config) error {
	var wg sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			ln.Close()
		case <-stop:
		}
	}()
	defer close(stop)
	defer wg.Wait()
	for {
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case s.active <- struct{}{}:
		default:
			raw.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-s.active }()
			defer raw.Close()
			done := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					raw.Close()
				case <-done:
				}
			}()
			defer close(done)
			c := tls.Server(raw, conf)
			_ = c.SetDeadline(time.Now().Add(Timeout))
			if c.HandshakeContext(ctx) != nil {
				return
			}
			s.serveConn(c)
		}()
	}
}

func (s *Store) serveConn(c net.Conn) {
	// Magic + epoch + capacity; a fresh client must not resume old page versions.
	hello := make([]byte, 24)
	copy(hello, "NXP1")
	copy(hello[4:], s.epoch[:])
	binary.BigEndian.PutUint32(hello[20:], uint32(len(s.data)))
	if writeFull(c, hello) != nil {
		return
	}
	for {
		_ = c.SetDeadline(time.Now().Add(30 * time.Second))
		var h [13]byte // op, page index, expected version
		if _, err := io.ReadFull(c, h[:]); err != nil {
			return
		}
		_ = c.SetDeadline(time.Now().Add(Timeout))
		id := binary.BigEndian.Uint32(h[1:5])
		expected := binary.BigEndian.Uint64(h[5:])
		if int(id) >= len(s.data) || (h[0] != 'G' && h[0] != 'P') {
			return
		}
		var input [PageSize]byte
		if h[0] == 'P' {
			if _, err := io.ReadFull(c, input[:]); err != nil {
				return
			}
		}
		s.mu.Lock()
		if s.versions[id] != expected {
			s.mu.Unlock()
			_ = writeFull(c, []byte{1})
			return
		}
		if h[0] == 'P' {
			if expected == ^uint64(0) {
				s.mu.Unlock()
				return
			}
			copy(s.data[id], input[:])
			s.versions[id]++
		}
		var reply [41]byte
		binary.BigEndian.PutUint64(reply[1:9], s.versions[id])
		digest := sha256.Sum256(s.data[id])
		copy(reply[9:], digest[:])
		// Only one accepted session can issue requests. Keeping the store lock
		// over bounded network writes avoids an extra whole-store snapshot.
		err := writeFull(c, reply[:])
		if err == nil && h[0] == 'G' {
			err = writeFull(c, s.data[id])
		}
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

type TransportStats struct {
	Gets              uint64 `json:"gets"`
	Puts              uint64 `json:"puts"`
	PageBytesReceived uint64 `json:"pageBytesReceived"`
	PageBytesSent     uint64 `json:"pageBytesSent"`
}

// Client never retries ambiguous writes. Any protocol/network error poisons the
// session; callers must stop instead of returning zeros or stale pages.
type Client struct {
	mu       sync.Mutex
	c        net.Conn
	pages    int
	versions []uint64
	hashes   [][32]byte
	failed   bool
	stats    TransportStats
}

func Dial(ctx context.Context, addr string, conf *tls.Config) (*Client, error) {
	if err := PrivateAddress(addr); err != nil {
		return nil, err
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: Timeout}, Config: conf}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("authenticated connection failed: %w", err)
	}
	_ = c.SetDeadline(time.Now().Add(Timeout))
	var h [24]byte
	if _, err = io.ReadFull(c, h[:]); err != nil {
		c.Close()
		return nil, err
	}
	n := int(binary.BigEndian.Uint32(h[20:]))
	if string(h[:4]) != "NXP1" || n < 2 || n > MaxPages {
		c.Close()
		return nil, errors.New("invalid donor greeting")
	}
	cl := &Client{c: c, pages: n, versions: make([]uint64, n), hashes: make([][32]byte, n)}
	zero := sha256.Sum256(make([]byte, PageSize))
	for i := range cl.hashes {
		cl.hashes[i] = zero
	}
	return cl, nil
}

func (c *Client) Pages() int            { return c.pages }
func (c *Client) Close() error          { return c.c.Close() }
func (c *Client) Stats() TransportStats { c.mu.Lock(); defer c.mu.Unlock(); return c.stats }

func (c *Client) exchange(ctx context.Context, op byte, page int, input []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return nil, errors.New("session failed; reconnect/retry prohibited")
	}
	if page < 0 || page >= c.pages || (op == 'P' && len(input) != PageSize) {
		return nil, errors.New("invalid page request")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.c.SetDeadline(deadline); err != nil {
		return nil, err
	}
	// Cancellation wakes a blocked socket without leaking a watcher goroutine.
	finished := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			c.c.Close()
		case <-finished:
		}
	}()
	defer func() { close(finished); <-watchDone }()
	fail := func(err error) ([]byte, error) { c.failed = true; c.c.Close(); return nil, err }
	var h [13]byte
	h[0] = op
	binary.BigEndian.PutUint32(h[1:5], uint32(page))
	binary.BigEndian.PutUint64(h[5:], c.versions[page])
	if err := writeFull(c.c, h[:]); err != nil {
		return fail(err)
	}
	if op == 'P' {
		if err := writeFull(c.c, input); err != nil {
			return fail(err)
		}
	}
	var status [1]byte
	if _, err := io.ReadFull(c.c, status[:]); err != nil {
		return fail(err)
	}
	if status[0] != 0 {
		return fail(errors.New("donor rejected page version"))
	}
	var meta [40]byte
	if _, err := io.ReadFull(c.c, meta[:]); err != nil {
		return fail(err)
	}
	wantVersion := c.versions[page]
	wantHash := c.hashes[page]
	if op == 'P' {
		wantVersion++
		wantHash = sha256.Sum256(input)
	}
	if binary.BigEndian.Uint64(meta[:8]) != wantVersion || string(meta[8:]) != string(wantHash[:]) {
		return fail(errors.New("page version or integrity mismatch"))
	}
	if op == 'P' {
		c.versions[page] = wantVersion
		c.hashes[page] = wantHash
		c.stats.Puts++
		c.stats.PageBytesSent += PageSize
		return nil, nil
	}
	output := make([]byte, PageSize)
	if _, err := io.ReadFull(c.c, output); err != nil {
		return fail(err)
	}
	if sha256.Sum256(output) != wantHash {
		return fail(errors.New("page payload integrity mismatch"))
	}
	c.stats.Gets++
	c.stats.PageBytesReceived += PageSize
	return output, nil
}
func (c *Client) Get(ctx context.Context, page int) ([]byte, error) {
	return c.exchange(ctx, 'G', page, nil)
}
func (c *Client) Put(ctx context.Context, page int, data []byte) error {
	_, err := c.exchange(ctx, 'P', page, data)
	return err
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

// Numeric loopback/RFC1918/ULA only: no wildcard listener, DNS or public route.
func PrivateAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return errors.New("use a numeric private IP:port")
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate()) || ip.IsUnspecified() {
		return errors.New("only numeric loopback/private addresses allowed")
	}
	return nil
}
