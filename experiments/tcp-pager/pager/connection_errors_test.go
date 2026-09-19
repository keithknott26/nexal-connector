package pager

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConnectionErrorCodesAreFixed(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{context.Canceled, "cancelled"},
		{context.DeadlineExceeded, "timeout"},
		{syscall.ECONNRESET, "connection_reset"},
		{syscall.ECONNREFUSED, "connection_refused"},
		{io.EOF, "connection_closed"},
		{io.ErrUnexpectedEOF, "connection_closed"},
		{x509.CertificateInvalidError{Reason: x509.Expired}, "certificate_time_invalid"},
		{x509.CertificateInvalidError{Reason: x509.IncompatibleUsage}, "certificate_invalid"},
		{x509.UnknownAuthorityError{}, "certificate_untrusted"},
		{x509.HostnameError{}, "certificate_name_invalid"},
		{errors.New("secret certificate contents"), "connection_rejected"},
	}
	for _, tc := range cases {
		if got := ConnectionErrorCode(fmt.Errorf("private context: %w", tc.err)); got != tc.code {
			t.Fatalf("got %s; want %s", got, tc.code)
		}
	}
}

func TestDialReportsTCPAndTLSPhases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dial(ctx, "127.0.0.1:1", &tls.Config{}); err == nil || !strings.Contains(err.Error(), "TCP connect failed [cancelled]") {
		t.Fatalf("unexpected TCP diagnostic: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := ln.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		var b [1]byte
		_ = c.SetDeadline(time.Now().Add(time.Second))
		_, _ = c.Read(b[:])
		_, _ = c.Write([]byte("not TLS"))
	}()
	keys, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := keys.ClientTLS()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = Dial(ctx, ln.Addr().String(), cfg); err == nil || !strings.Contains(err.Error(), "TLS handshake failed [") {
		t.Fatalf("unexpected TLS diagnostic: %v", err)
	}
	<-done
}

func TestLabDiagnosticsBoundedAndAuthenticationPreserved(t *testing.T) {
	keys, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	server, _ := keys.ServerTLS()
	client, _ := keys.ClientTLS()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type event struct{ phase, code string }
	var events []event
	done := make(chan error, 1)
	go func() {
		done <- ServeLabObserved(ctx, ln, server, 64, 1, func(phase, code string) {
			events = append(events, event{phase, code})
		})
	}()
	// Raw TCP probes do not consume the authenticated-session budget.
	for n := 0; n < 20; n++ {
		c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte("not TLS"))
		c.Close()
	}
	c, err := Dial(ctx, ln.Addr().String(), client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Get(ctx, 0); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if len(events) != 33 || events[32] != (event{"diagnostics", "further_events_suppressed"}) {
		t.Fatalf("invalid bounded diagnostics: %v", events)
	}
}

func TestLabReportsSuccessfulMutualTLS(t *testing.T) {
	keys, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	server, _ := keys.ServerTLS()
	client, _ := keys.ClientTLS()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []string
	done := make(chan error, 1)
	go func() {
		done <- ServeLabObserved(ctx, ln, server, 64, 1, func(phase, code string) {
			events = append(events, phase+":"+code)
		})
	}()
	c, err := Dial(ctx, ln.Addr().String(), client)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Join(events, ",") != "tcp_accept:accepted,tls_handshake:authenticated" {
		t.Fatalf("unexpected success diagnostics: %v", events)
	}
}
