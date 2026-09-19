package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
)

var errRedirectForbidden = errors.New("coordinator redirects are forbidden")

// Only fixed messages leave this boundary. Underlying errors can contain URLs,
// proxy credentials, certificate names or attacker-controlled response text.
func transportError(err error) error {
	kind := "network"
	hint := "connection failed; check network access and outbound firewall rules"
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var record tls.RecordHeaderError
	var network net.Error
	switch {
	case errors.Is(err, errRedirectForbidden):
		kind, hint = "redirect", "redirect refused; use the coordinator's direct HTTPS origin"
	case errors.Is(err, context.Canceled):
		kind, hint = "cancelled", "request cancelled"
	case errors.As(err, &dns):
		kind, hint = "dns", "hostname lookup failed; check DNS and network connectivity"
	case errors.As(err, &cert), errors.As(err, &unknown), errors.As(err, &hostname), errors.As(err, &invalid):
		kind, hint = "tls_certificate", "certificate verification failed; check the Mac clock and trusted certificate configuration; do not disable verification"
	case errors.As(err, &record):
		kind, hint = "tls_protocol", "TLS handshake received an invalid protocol response"
	case errors.Is(err, context.DeadlineExceeded):
		kind, hint = "timeout", "request timed out"
	case errors.As(err, &network) && network.Timeout():
		kind, hint = "timeout", "connection or response timed out"
	case errors.Is(err, syscall.ECONNREFUSED):
		kind, hint = "connection_refused", "connection refused by the destination or network"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		kind, hint = "unreachable", "no working route to the coordinator"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		kind, hint = "connection_closed", "connection closed before a complete response"
	}
	return fmt.Errorf("coordinator request failed [%s]: %s; automatic proxy settings are not used", kind, hint)
}

// CheckConnectivity deliberately sends neither enrollment codes nor host tokens,
// even if the caller constructed this client with a credential.
func (c *Client) CheckConnectivity(ctx context.Context) error {
	anonymous := *c
	anonymous.token = ""
	var out struct {
		Status  string `json:"status"`
		Mode    string `json:"mode"`
		Version string `json:"version"`
	}
	if err := anonymous.call(ctx, "GET", "/api/health", nil, &out); err != nil {
		return err
	}
	if out.Status != "ok" || (out.Mode != "development" && out.Mode != "production") || out.Version == "" {
		return errors.New("coordinator health response is not ready or has an invalid schema")
	}
	return nil
}
