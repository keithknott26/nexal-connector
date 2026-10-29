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

func TestRunPodRecordValidate(t *testing.T) {
	var nilRec *RunPodRecord
	if err := nilRec.validate(); err != nil {
		t.Fatalf("nil record: %v", err)
	}
	good := RunPodRecord{EndpointID: "abc-123_X", TemplateID: "tpl_1", Model: "Qwen/Qwen3-32B", Preset: "fast", MaxWorkers: 10}
	if err := good.validate(); err != nil {
		t.Fatalf("good record: %v", err)
	}
	noTemplate := RunPodRecord{EndpointID: "abc"}
	if err := noTemplate.validate(); err != nil {
		t.Fatalf("empty template id should be allowed: %v", err)
	}
	long := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = 'a'
		}
		return string(b)
	}
	for name, rec := range map[string]RunPodRecord{
		"empty endpoint":      {},
		"endpoint with slash": {EndpointID: "a/b"},
		"endpoint with space": {EndpointID: "a b"},
		"endpoint too long":   {EndpointID: long(65)},
		"template with dot":   {EndpointID: "abc", TemplateID: "t.1"},
		"model too long":      {EndpointID: "abc", Model: long(201)},
		"preset too long":     {EndpointID: "abc", Preset: long(41)},
		"negative workers":    {EndpointID: "abc", MaxWorkers: -1},
		"too many workers":    {EndpointID: "abc", MaxWorkers: 11},
	} {
		if rec.validate() == nil {
			t.Errorf("%s: accepted %+v", name, rec)
		}
	}
	if err := (&RunPodRecord{EndpointID: long(64)}).validate(); err != nil {
		t.Errorf("64-char endpoint id rejected: %v", err)
	}
}
