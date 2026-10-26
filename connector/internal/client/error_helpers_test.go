package client

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrorDetailParsesAndSanitizes(t *testing.T) {
	code, msg := errorDetail(strings.NewReader(`{"error":{"code":"host_revoked","message":"gone\u0000 now\nplease"}}`))
	if code != "host_revoked" || msg != "gone now please" {
		t.Fatalf("code=%q msg=%q", code, msg)
	}
	// An unsafe code is dropped but the message survives.
	code, msg = errorDetail(strings.NewReader(`{"error":{"code":"Bad-Code","message":"x"}}`))
	if code != "" || msg != "x" {
		t.Fatalf("unsafe code: code=%q msg=%q", code, msg)
	}
	for _, body := range []string{"", "not json", "<html>502</html>"} {
		if c, m := errorDetail(strings.NewReader(body)); c != "" || m != "" {
			t.Errorf("body %q: code=%q msg=%q", body, c, m)
		}
	}
}

func TestPairingErrorMapsStatuses(t *testing.T) {
	for status, want := range map[int]string{401: "HTTP 401", 403: "HTTP 403", 404: "HTTP 404", 503: "HTTP 503"} {
		err := pairingError(fmt.Errorf("pair: %w", &StatusError{Status: status}))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("status %d: got %v", status, err)
		}
	}
	orig := &StatusError{Status: 500}
	if got := pairingError(orig); got != error(orig) {
		t.Errorf("unmapped status must pass through, got %v", got)
	}
	plain := errors.New("dial tcp: refused")
	if got := pairingError(plain); got != plain {
		t.Errorf("non-status error must pass through, got %v", got)
	}
}

func TestHostRoutePaths(t *testing.T) {
	for name, fn := range map[string]func(string, string) (string, error){"remoteInference": remoteInferencePath, "sandbox": sandboxPath} {
		got, err := fn("host_1", "leaf")
		if err != nil || got != "/api/v2/hosts/host_1/leaf" {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
		for _, bad := range []string{"", "a/b", "../x", "has space"} {
			if _, err := fn(bad, "leaf"); err == nil {
				t.Errorf("%s: host id %q must be rejected", name, bad)
			}
		}
	}
}
