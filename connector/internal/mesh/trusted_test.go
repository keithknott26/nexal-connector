package mesh

import (
	"context"
	"testing"
)

func TestTrustedExecutableRejectsUnknownName(t *testing.T) {
	for _, name := range []string{"", "netbird", "../nexal-network", "sh"} {
		if path, err := trustedExecutable(name); err == nil || path != "" {
			t.Errorf("trustedExecutable(%q) = (%q, %v), want error", name, path, err)
		}
	}
}

func TestPeerAddressByNameBlankName(t *testing.T) {
	for _, name := range []string{"", "   ", "."} {
		if ip, ok := PeerAddressByName(context.Background(), name); ok || ip != "" {
			t.Errorf("PeerAddressByName(%q) = (%q, %v), want empty", name, ip, ok)
		}
	}
}

func TestStaticPlanStoreLoad(t *testing.T) {
	want := StartupPlan{}
	got, err := StaticPlanStore{Plan: want}.Load(context.Background())
	if err != nil || got != want {
		t.Errorf("Load() = (%+v, %v), want (%+v, nil)", got, err, want)
	}
}
