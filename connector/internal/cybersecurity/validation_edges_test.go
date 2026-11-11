package cybersecurity

import (
	"testing"
	"time"
)

func TestValidationRunValidateEdges(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		change func(*ValidationRun)
		ok     bool
	}{
		{"canary ok", func(r *ValidationRun) {}, true},
		{"bad nonce", func(r *ValidationRun) { r.Nonce = "nope" }, false},
		{"uppercase id not canonical", func(r *ValidationRun) { r.ID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" }, false},
		{"already expired", func(r *ValidationRun) { r.ExpiresAt = now.Add(-time.Second) }, false},
		{"exactly now", func(r *ValidationRun) { r.ExpiresAt = now }, false},
		{"deadline too far", func(r *ValidationRun) { r.ExpiresAt = now.Add(126 * time.Second) }, false},
		{"cgnat low edge", func(r *ValidationRun) { r.Module = "network_canary"; r.TargetIP = "100.64.0.0" }, true},
		{"cgnat high edge", func(r *ValidationRun) { r.Module = "network_canary"; r.TargetIP = "100.127.255.255" }, true},
		{"below cgnat", func(r *ValidationRun) { r.Module = "network_canary"; r.TargetIP = "100.63.255.255" }, false},
		{"above cgnat", func(r *ValidationRun) { r.Module = "network_canary"; r.TargetIP = "100.128.0.0" }, false},
		{"empty target", func(r *ValidationRun) { r.Module = "network_canary" }, false},
		{"hostname target", func(r *ValidationRun) { r.Module = "network_canary"; r.TargetIP = "example.com" }, false},
	}
	for _, c := range cases {
		r := validationGrant()
		r.ExpiresAt = now.Add(time.Minute)
		c.change(&r)
		if err := r.Validate(now); (err == nil) != c.ok {
			t.Errorf("%s: Validate err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}
