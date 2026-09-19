package pager

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"syscall"
)

// ConnectionErrorCode returns fixed labels only, never peer-provided certificate
// names, addresses, error strings, key material or packet contents.
func ConnectionErrorCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		if invalid.Reason == x509.Expired {
			return "certificate_time_invalid"
		}
		return "certificate_invalid"
	}
	var authority x509.UnknownAuthorityError
	if errors.As(err, &authority) {
		return "certificate_untrusted"
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return "certificate_name_invalid"
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return "connection_reset"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "connection_refused"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "connection_closed"
	}
	return "connection_rejected"
}
