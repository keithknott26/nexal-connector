package client

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestTLSConfigClonesTransportSettingsAndKeepsTLS13Floor(t *testing.T) {
	c, err := New("https://coordinator.example", "token", false)
	if err != nil {
		t.Fatal(err)
	}
	got := c.tlsConfig()
	if got == nil || got.MinVersion != tls.VersionTLS13 {
		t.Fatalf("tlsConfig = %+v, want TLS 1.3 floor", got)
	}
	// Mutating the returned config must not weaken the REST transport.
	got.MinVersion = tls.VersionTLS10
	if again := c.tlsConfig(); again == nil || again.MinVersion != tls.VersionTLS13 {
		t.Fatalf("tlsConfig is not a clone: %+v", again)
	}
}

func TestTLSConfigNilWithoutTransportTLSSettings(t *testing.T) {
	c, err := New("https://coordinator.example", "token", false)
	if err != nil {
		t.Fatal(err)
	}
	c.http = &http.Client{Transport: &http.Transport{}}
	if got := c.tlsConfig(); got != nil {
		t.Fatalf("tlsConfig = %+v, want nil", got)
	}
	c.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })}
	if got := c.tlsConfig(); got != nil {
		t.Fatalf("tlsConfig = %+v for non-http transport, want nil", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
