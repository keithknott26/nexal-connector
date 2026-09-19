package pager

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestLabRequiresBoundsAndDeadline(t *testing.T) {
	for _, pair := range [][2]int{{1, 1}, {257, 1}, {64, 0}, {64, 17}, {64, 1}} {
		if err := ServeLab(context.Background(), nil, nil, pair[0], pair[1]); err == nil {
			t.Fatal("unbounded lab accepted")
		}
	}
}

func TestLabCancelClosesStalledHandshake(t *testing.T) {
	keys, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	conf, err := keys.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeLab(ctx, ln, conf, 64, 4) }()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled handshake survived cancellation")
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := c.Read(b[:]); err == nil {
		t.Fatal("connection remained open")
	}
}

func TestLabBadTLSDoesNotConsumeAuthenticatedSession(t *testing.T) {
	k, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	st, _ := k.ServerTLS()
	ct, _ := k.ClientTLS()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeLab(ctx, ln, st, 64, 1) }()
	bad, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bad.Write([]byte("not TLS\n"))
	bad.Close()
	c, err := Dial(ctx, ln.Addr().String(), ct)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Get(ctx, 0); err != nil {
		t.Fatal(err)
	}
	c.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("authenticated session cap not reached")
	}
}
