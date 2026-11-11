package smbshare

import (
	"testing"
	"time"
)

func TestOptionsHealthDefaultsAndOverrides(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		o := Options{HealthGrace: d, HealthInterval: d}
		if got := o.grace(); got != 20*time.Second {
			t.Errorf("grace(%v) = %v, want default 20s", d, got)
		}
		if got := o.interval(); got != 2*time.Second {
			t.Errorf("interval(%v) = %v, want default 2s", d, got)
		}
	}
	o := Options{HealthGrace: 5 * time.Second, HealthInterval: 750 * time.Millisecond}
	if o.grace() != 5*time.Second || o.interval() != 750*time.Millisecond {
		t.Errorf("explicit values not honoured: %v %v", o.grace(), o.interval())
	}
}
