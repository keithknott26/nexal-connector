package mesh

import "testing"

func TestPeerAddressFromStatus(t *testing.T) {
	status := []byte(`{"peers":{"details":[
		{"fqdn":"Studio.mesh.example.","netbirdIp":"100.64.0.7/16"},
		{"fqdn":"empty.mesh.example","netbirdIp":""},
		{"fqdn":"bare.mesh.example","netbirdIp":"100.64.0.9"}]}}`)
	cases := []struct {
		name, want, ip string
		ok             bool
	}{
		{"strips prefix and ignores case/trailing dot", "studio.mesh.example", "100.64.0.7", true},
		{"address without prefix", "bare.mesh.example", "100.64.0.9", true},
		{"peer without address", "empty.mesh.example", "", false},
		{"unknown peer", "nope.mesh.example", "", false},
	}
	for _, c := range cases {
		ip, ok := peerAddressFromStatus(status, c.want)
		if ip != c.ip || ok != c.ok {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, ip, ok, c.ip, c.ok)
		}
	}
	if ip, ok := peerAddressFromStatus([]byte("not json"), "x"); ok || ip != "" {
		t.Errorf("malformed status: got (%q, %v)", ip, ok)
	}
}
