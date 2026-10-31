package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"nexal/connector/internal/config"
)

type memSecrets map[string]string

func (m memSecrets) Get(_ context.Context, n string) (string, error) {
	if v, ok := m[n]; ok {
		return v, nil
	}
	return "", errors.New("none")
}
func (m memSecrets) Put(_ context.Context, n, v string) error { m[n] = v; return nil }
func (m memSecrets) Delete(_ context.Context, n string) error { delete(m, n); return nil }

func TestBuildOverflow(t *testing.T) {
	ctx := context.Background()
	gpu := &config.OverflowTier{Name: "gpu", BaseURL: "https://api.runpod.ai/v2/x/openai/v1", Model: "m", FirstByteSeconds: 30}
	on := config.Config{Overflow: &config.OverflowConfig{Enabled: true, GPU: gpu, GPUPeers: []string{"10.0.0.2"}, MaxWaitSeconds: 60, DailyLimit: 5}}
	if buildOverflow(ctx, config.Config{}, memSecrets{}) != nil {
		t.Fatal("absent config must be off")
	}
	if buildOverflow(ctx, on, memSecrets{}) != nil {
		t.Fatal("a tier without a key must not be used")
	}
	ov := buildOverflow(ctx, on, memSecrets{"overflow-gpu": "0123456789abcdef"})
	if ov == nil || ov.GPU == nil || ov.Cloud != nil || !ov.GPUPeers["10.0.0.2"] || ov.GPUPeers["10.0.0.3"] || ov.DailyLimit != 5 {
		t.Fatalf("%+v", ov)
	}
	on.Overflow.Enabled = false
	off := buildOverflow(ctx, on, memSecrets{"overflow-gpu": "0123456789abcdef"})
	if off == nil || off.Enabled {
		t.Fatal("disabled keeps the tier ready but does not offload")
	}
	if tiers, _, _ := off.Plan("10.0.0.2", "m", 1<<20, 0); len(tiers) != 0 {
		t.Fatal("disabled must not offload")
	}
}

func TestOverflowStatusNeverCarriesKeys(t *testing.T) {
	st := overflowStatus(context.Background(), config.Config{}, memSecrets{"overflow-gpu": "SECRETSECRETSECRET"})
	if st["gpuKeyStored"] != true || st["cloudKeyStored"] != false {
		t.Fatal(st)
	}
	if b, _ := json.Marshal(st); strings.Contains(string(b), "SECRETSECRET") {
		t.Fatal("key leaked into status")
	}
}

func TestSplitListTrimsAndDropsEmpties(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{" , ,", nil},
		{"a", []string{"a"}},
		{" a , b,,c ", []string{"a", "b", "c"}},
	}
	for _, c := range cases {
		got := splitList(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("splitList(%q) = %q, want %q", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("splitList(%q) = %q, want %q", c.in, got, c.want)
			}
		}
	}
}

func TestOverflowCommandRejectsBadInvocationBeforeTouchingConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := t.TempDir() + "/missing.json"
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no action", nil, "usage: nexal overflow"},
		{"unknown flag", []string{"status", "--config", cfg, "--bogus"}, "invalid command flags"},
		{"positional argument", []string{"status", "--config", cfg, "extra"}, "unexpected positional arguments"},
	} {
		err := overflowCommand(context.Background(), c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
	}
}
