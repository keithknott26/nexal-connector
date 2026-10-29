package config

import "testing"

func TestOverflowConfigValidate(t *testing.T) {
	gpu := &OverflowTier{Name: "gpu", BaseURL: "https://api.runpod.ai/v2/abc/openai/v1", Model: "Qwen/Qwen3-32B"}
	ok := func(o *OverflowConfig) {
		t.Helper()
		if err := o.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	bad := func(o *OverflowConfig) {
		t.Helper()
		if o.Validate() == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	ok(nil)
	ok(&OverflowConfig{})
	ok(&OverflowConfig{Enabled: true, GPU: gpu, GPUPeers: []string{"10.20.0.5", "*"}})
	bad(&OverflowConfig{Enabled: true})
	bad(&OverflowConfig{GPU: &OverflowTier{Name: "gpu", BaseURL: "http://x.example/v1", Model: "m"}})
	bad(&OverflowConfig{GPU: &OverflowTier{Name: "gpu", BaseURL: "https://u:p@x.example/v1", Model: "m"}})
	bad(&OverflowConfig{GPU: &OverflowTier{Name: "gpu", BaseURL: "https://x.example/v1?key=1", Model: "m"}})
	bad(&OverflowConfig{GPU: &OverflowTier{Name: "", BaseURL: "https://x.example/v1", Model: "m"}})
	bad(&OverflowConfig{GPU: gpu, GPUPeers: []string{"not-an-ip"}})
	bad(&OverflowConfig{GPU: gpu, DailyLimit: -1})
}
