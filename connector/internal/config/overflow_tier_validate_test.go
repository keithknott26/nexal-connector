package config

import (
	"strings"
	"testing"
)

func goodTier() *OverflowTier {
	return &OverflowTier{Name: "gpu", BaseURL: "https://api.example.com/v1", Model: "m", FirstByteSeconds: 90}
}

func TestOverflowTierValidateRejectsBadFields(t *testing.T) {
	if err := (*OverflowTier)(nil).validate("x"); err != nil {
		t.Fatalf("nil tier: %v", err)
	}
	if err := goodTier().validate("x"); err != nil {
		t.Fatalf("good tier: %v", err)
	}
	cases := []struct {
		want   string
		mutate func(*OverflowTier)
	}{
		{"model is required", func(o *OverflowTier) { o.Model = "  " }},
		{"name is required", func(o *OverflowTier) { o.Name = "a\nb" }},
		{"name is required", func(o *OverflowTier) { o.Name = strings.Repeat("n", 41) }},
		{"firstByteSeconds", func(o *OverflowTier) { o.FirstByteSeconds = 601 }},
		{"firstByteSeconds", func(o *OverflowTier) { o.FirstByteSeconds = -1 }},
	}
	for i, c := range cases {
		tier := goodTier()
		c.mutate(tier)
		err := tier.validate("overflow gpu")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("case %d: want %q, got %v", i, c.want, err)
		}
	}
}

func TestOverflowConfigValidateEdges(t *testing.T) {
	if err := (*OverflowConfig)(nil).Validate(); err != nil {
		t.Fatalf("nil: %v", err)
	}
	bad := []*OverflowConfig{
		{MaxWaitSeconds: -1},
		{DailyLimit: -1},
		{HardAlias: "a\nb"},
		{HardAlias: strings.Repeat("a", 101)},
		{GPU: &OverflowTier{Name: "g", BaseURL: "http://x/v1", Model: "m"}},
		{Cloud: &OverflowTier{Name: "c", BaseURL: "ftp://x", Model: "m"}},
		{GPUPeers: []string{"host"}},
		{CloudPeers: []string{"host"}},
		{Enabled: true},
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: want error", i)
		}
	}
	ok := &OverflowConfig{Enabled: true, GPU: goodTier(), GPUPeers: []string{"*"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
}
