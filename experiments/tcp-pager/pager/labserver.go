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
	return ServeLabObserved(ctx, ln, conf, pages, sessions, nil)
}

// ServeLabObserved reports at most 32 fixed-label events plus a suppression
// notice. The synchronous callback must return promptly. No peer data is emitted.
func ServeLabObserved(ctx context.Context, ln net.Listener, conf *tls.Config, pages, sessions int, report func(string, string)) error {
	if pages < 2 || pages > MaxPages || sessions < 1 || sessions > 16 {
		return errors.New("invalid lab capacity/session bound")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("lab server requires a lifetime deadline")
	}
	defer ln.Close()
	events := 0
	emit := func(phase, result string) {
		if report == nil {
			return
		}
		if events < 32 {
			report(phase, result)
		} else if events == 32 {
			report("diagnostics", "further_events_suppressed")
		}
		if events <= 32 {
			events++
		}
	}
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
		emit("tcp_accept", "accepted")
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
				emit("tls_handshake", ConnectionErrorCode(err))
				return nil
			}
			emit("tls_handshake", "authenticated")
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
