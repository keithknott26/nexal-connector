package pager

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"
)

// ServeLab is a disposable acceptance fixture, NOT a resumable memory service.
// Each authenticated connection gets a new zero-filled store and epoch. Serving
// connections serially bounds allocated page payload to one store. Invalid TLS
// attempts do not allocate a store or consume the authenticated-session budget.
// The caller must supply a deadline, and must not expose this fixture publicly.
func ServeLab(ctx context.Context, ln net.Listener, conf *tls.Config, pages, sessions int) error {
	if pages < 2 || pages > MaxPages || sessions < 1 || sessions > 16 {
		return errors.New("invalid lab capacity/session bound")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("lab server requires a lifetime deadline")
	}
	defer ln.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			ln.Close()
		case <-done:
		}
	}()
	for accepted := 0; accepted < sessions; {
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		err = func() error {
			connDone := make(chan struct{})
			defer close(connDone)
			defer raw.Close()
			go func() {
				select {
				case <-ctx.Done():
					raw.Close()
				case <-connDone:
				}
			}()
			c := tls.Server(raw, conf)
			_ = c.SetDeadline(time.Now().Add(Timeout))
			if err := c.HandshakeContext(ctx); err != nil {
				return nil
			}
			accepted++
			store, err := NewStore(pages)
			if err != nil {
				return err
			}
			store.serveConn(c)
			// Erase accessible payload before relinquishing the fixture. This is
			// best effort, not a guarantee about OS swap or runtime copies.
			for _, page := range store.data {
				clear(page)
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}
