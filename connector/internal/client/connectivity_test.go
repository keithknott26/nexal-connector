package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
)

func TestTransportErrorsAreClassifiedWithoutSecrets(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{&net.DNSError{Err: "secret", Name: "secret", Server: "secret"}, "dns"},
		{&tls.CertificateVerificationError{Err: errors.New("secret")}, "tls_certificate"},
		{x509.UnknownAuthorityError{}, "tls_certificate"},
		{x509.HostnameError{Host: "secret"}, "tls_certificate"},
		{x509.CertificateInvalidError{}, "tls_certificate"},
		{tls.RecordHeaderError{Msg: "secret"}, "tls_protocol"},
		{context.DeadlineExceeded, "timeout"},
		{context.Canceled, "cancelled"},
		{syscall.ECONNREFUSED, "connection_refused"},
		{syscall.ENETUNREACH, "unreachable"},
		{syscall.EHOSTUNREACH, "unreachable"},
		{syscall.ECONNRESET, "connection_closed"},
		{io.EOF, "connection_closed"},
		{io.ErrUnexpectedEOF, "connection_closed"},
		{errRedirectForbidden, "redirect"},
		{errors.New("secret"), "network"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			err := transportError(&url.Error{Op: "secret", URL: "https://secret:secret@secret", Err: tc.err})
			if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "["+tc.kind+"]") {
				t.Fatalf("unsafe or incorrect diagnostic: %s", err)
			}
		})
	}
}

func TestConnectivityIsCredentialFreeAndStrict(t *testing.T) {
	for _, body := range []string{
		`{"status":"ok","mode":"production","version":"0.1"}`,
		`{"status":"bad","mode":"production","version":"0.1"}`,
		`{"status":"ok"}`,
		`{"status":"ok","status":"bad","mode":"production","version":"0.1"}`,
		`<html>login</html>`,
		strings.Repeat("x", 65537),
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" || r.URL.Path != "/api/health" || r.Header.Get("Authorization") != "" || r.ContentLength > 0 {
				t.Error("preflight leaked credentials or performed a mutation")
			}
			_, _ = io.WriteString(w, body)
		}))
		c, _ := New(srv.URL, "secret-host-token", true)
		err := c.CheckConnectivity(context.Background())
		srv.Close()
		if (err == nil) != (body == `{"status":"ok","mode":"production","version":"0.1"}`) {
			t.Fatal("incorrect readiness result")
		}
		if c.token != "secret-host-token" {
			t.Fatal("preflight mutated caller's credential")
		}
	}
}

func TestConnectivityCertificateFailureIsExplicit(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("untrusted server received request")
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "", false)
	err := c.CheckConnectivity(context.Background())
	if err == nil || !strings.Contains(err.Error(), "[tls_certificate]") {
		t.Fatalf("missing certificate diagnosis: %v", err)
	}
}

func TestEnrollmentConflictDiffersFromConnectionFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, "secret-server-response")
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "", true)
	_, err := c.Enroll(context.Background(), Enrollment{Code: "secret"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "invitation") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("incorrect conflict diagnostic: %v", err)
	}
	if err := c.CheckConnectivity(context.Background()); err == nil || strings.Contains(err.Error(), "invitation") {
		t.Fatal("health conflict mislabeled as enrollment error")
	}
}
