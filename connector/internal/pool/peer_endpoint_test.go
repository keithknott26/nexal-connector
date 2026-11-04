package pool

import (
	"errors"
	"testing"
)

// ValidPeerEndpoint is the single endpoint rule shared by NewPeerClient and
// static-peer config; pin its accept and reject sets directly.
func TestValidPeerEndpointRule(t *testing.T) {
	for _, ep := range []string{
		"https://10.0.0.5:8443",
		"https://192.168.1.2:443/",
		"https://127.0.0.1:9000",
		"https://169.254.1.1:9000",
		"https://[::1]:9000",
		"https://[fd00::1]:9000",
	} {
		if err := ValidPeerEndpoint(ep); err != nil {
			t.Errorf("%q should be accepted: %v", ep, err)
		}
	}
	for _, ep := range []string{
		"http://10.0.0.5:8443",       // not https
		"https://10.0.0.5",           // no port
		"https://8.8.8.8:443",        // public address
		"https://example.com:443",    // hostname, not an IP literal
		"https://0.0.0.0:443",        // unspecified
		"https://224.0.0.1:443",      // multicast
		"https://user@10.0.0.5:443",  // userinfo
		"https://10.0.0.5:443/path",  // path
		"https://10.0.0.5:443/?q=1",  // query
		"https://10.0.0.5:443/#frag", // fragment
		"",                           // empty
		"://bad",                     // unparsable
	} {
		if err := ValidPeerEndpoint(ep); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("%q should be ErrUnsafePath, got %v", ep, err)
		}
	}
	for _, ep := range []string{"https://10.0.0.5:0", "https://10.0.0.5:70000"} {
		if err := ValidPeerEndpoint(ep); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q should be ErrInvalid, got %v", ep, err)
		}
	}
}
