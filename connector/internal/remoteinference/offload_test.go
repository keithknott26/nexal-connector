package remoteinference

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"io"
	"log/slog"
	"sync"
	"testing"
)

type offloadCoord struct {
	mu    sync.Mutex
	tasks []Task
	notes []string
	keys  []string
}

func (o *offloadCoord) RemoteInferenceTasks(context.Context, string) ([]Task, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Task(nil), o.tasks...), nil
}
func (o *offloadCoord) ReportRemoteInference(context.Context, string, Report) error { return nil }
func (o *offloadCoord) ReportRemoteOffload(_ context.Context, _, _, note, publicKey string) error {
	o.mu.Lock()
	o.notes = append(o.notes, note)
	o.keys = append(o.keys, publicKey)
	o.mu.Unlock()
	return nil
}

func quietRunner(c Coordinator, ov *Overflow) *Runner {
	return &Runner{Coord: c, HostID: "m4", Overflow: ov, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestOverflowApplySwitchesModesLive(t *testing.T) {
	m := NewMeter()
	ov := &Overflow{GPU: &Tier{Name: "RunPod"}}
	ov.SetMeter(m)
	if tiers, _, _ := ov.Plan("10.0.0.9", "x", 100, 0); len(tiers) != 0 {
		t.Fatal("off until a policy says otherwise")
	}
	if note := ov.Apply(OffloadPolicy{Mode: "always"}); note != "Applied: every request goes to RunPod." {
		t.Fatal(note)
	}
	if tiers, _, _ := ov.Plan("10.0.0.9", "x", 10, 0); len(tiers) != 1 {
		t.Fatal("always must send a small request to the GPU, for any peer when none were configured")
	}
	ov.Apply(OffloadPolicy{Mode: "large", MaxPromptTokens: 6000})
	if tiers, _, _ := ov.Plan("10.0.0.9", "x", 10, 0); len(tiers) != 0 {
		t.Fatal("a small prompt stays local")
	}
	if tiers, _, _ := ov.Plan("10.0.0.9", "x", 7000, 0); len(tiers) != 1 {
		t.Fatal("a long prompt goes to the GPU")
	}
	if m.Snapshot().MaxPromptTokens != 6000 {
		t.Fatal("the graph's red line follows the limit")
	}
	ov.Apply(OffloadPolicy{Mode: "off"})
	if tiers, _, _ := ov.Plan("10.0.0.9", "x", 1<<20, 0); len(tiers) != 0 {
		t.Fatal("off answers locally")
	}
}

func TestOverflowApplyKeepsConfiguredPeers(t *testing.T) {
	ov := &Overflow{GPU: &Tier{Name: "RunPod"}, GPUPeers: map[string]bool{"10.0.0.2": true}}
	ov.Apply(OffloadPolicy{Mode: "always"})
	if tiers, _, _ := ov.Plan("10.0.0.3", "x", 10, 0); len(tiers) != 0 {
		t.Fatal("a peer the owner did not allow must not be offloaded")
	}
}

func TestRunnerAppliesAPolicyOnceAndAnswersIt(t *testing.T) {
	c := &offloadCoord{tasks: []Task{{ID: "rinf_1", TargetHostID: "m4", State: "ready", ModelID: "qwen",
		Offload: &OffloadPolicy{Mode: "always", SetAt: "2026-10-31T22:00:00.000Z"}}}}
	ov := &Overflow{GPU: &Tier{Name: "RunPod"}}
	r := quietRunner(c, ov)
	r.applyOffload(context.Background(), c.tasks)
	r.applyOffload(context.Background(), c.tasks)
	if len(c.notes) != 1 || c.notes[0] != "Applied: every request goes to RunPod." {
		t.Fatalf("one answer per change: %v", c.notes)
	}
	c.tasks[0].Offload = &OffloadPolicy{Mode: "off", SetAt: "2026-10-31T22:05:00.000Z"}
	r.applyOffload(context.Background(), c.tasks)
	if len(c.notes) != 2 || ov.Enabled {
		t.Fatalf("a newer policy applies: %v enabled=%v", c.notes, ov.Enabled)
	}
}

func TestRunnerSaysWhenThereIsNoGPU(t *testing.T) {
	c := &offloadCoord{tasks: []Task{{ID: "rinf_1", TargetHostID: "m4", State: "ready",
		Offload: &OffloadPolicy{Mode: "always", SetAt: "a"}}}}
	quietRunner(c, nil).applyOffload(context.Background(), c.tasks)
	if len(c.notes) != 1 || c.notes[0] != noGPUNote {
		t.Fatalf("%v", c.notes)
	}
}

// A compile-time check: the fake must satisfy the optional reporter, or the runner silently never answers.
var _ offloadReporter = (*offloadCoord)(nil)

func TestSealOpensOnlyForItsRecipient(t *testing.T) {
	m4, _ := ecdh.X25519().GenerateKey(rand.Reader)
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	sender, sealed, err := Seal(PublicKey(m4), "rpa_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(m4, sender, sealed); err != nil || got != "rpa_SECRET" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Open(other, sender, sealed); err == nil {
		t.Fatal("another key must not open it")
	}
	if _, _, err := Seal("not-a-key", "x"); err == nil {
		t.Fatal("a bad recipient key must be refused")
	}
}

func TestAMacWithoutAGPUPublishesItsKeyThenUsesTheSharedTier(t *testing.T) {
	key, _ := ecdh.X25519().GenerateKey(rand.Reader)
	c := &offloadCoord{tasks: []Task{{ID: "rinf_1", TargetHostID: "m4", State: "ready",
		Offload: &OffloadPolicy{Mode: "always", SetAt: "1"}}}}
	ov := &Overflow{}
	r := quietRunner(c, ov)
	r.BoxKey = func() (*ecdh.PrivateKey, error) { return key, nil }
	r.applyOffload(context.Background(), c.tasks)
	if c.notes[0] != noGPUNote || c.keys[0] != PublicKey(key) {
		t.Fatalf("no GPU: say so and publish the key: %v %v", c.notes, c.keys)
	}
	sender, sealed, _ := Seal(c.keys[0], "rpa_SECRET")
	c.tasks[0].Offload = &OffloadPolicy{Mode: "always", SetAt: "2", GPU: &SharedGPU{Name: "RunPod",
		BaseURL: "https://api.runpod.ai/v2/ep1/openai/v1", Model: "Qwen/Qwen3-14B-AWQ", FirstByteSeconds: 180, SenderKey: sender, SealedKey: sealed}}
	r.applyOffload(context.Background(), c.tasks)
	if c.notes[1] != "Applied: every request goes to RunPod." || c.keys[1] != "" {
		t.Fatalf("%v %v", c.notes, c.keys)
	}
	if ov.GPU == nil || ov.GPU.APIKey != "rpa_SECRET" || ov.HasOwnGPU() {
		t.Fatalf("the shared tier is in use, marked as shared: %+v", ov.GPU)
	}
	if tiers, _, _ := ov.Plan("10.0.0.9", "x", 10, 0); len(tiers) != 1 {
		t.Fatal("requests now go to the shared GPU")
	}
}

func TestASharedTierNeverReplacesThisMacsOwn(t *testing.T) {
	key, _ := ecdh.X25519().GenerateKey(rand.Reader)
	own := &Tier{Name: "Mine", APIKey: "own"}
	sender, sealed, _ := Seal(PublicKey(key), "theirs")
	c := &offloadCoord{tasks: []Task{{ID: "rinf_1", TargetHostID: "m4", State: "ready", Offload: &OffloadPolicy{Mode: "always", SetAt: "1",
		GPU: &SharedGPU{Name: "RunPod", BaseURL: "https://x/v1", SenderKey: sender, SealedKey: sealed}}}}}
	ov := &Overflow{GPU: own}
	r := quietRunner(c, ov)
	r.BoxKey = func() (*ecdh.PrivateKey, error) { return key, nil }
	r.applyOffload(context.Background(), c.tasks)
	if ov.GPU != own {
		t.Fatal("this Mac's own tier stays")
	}
}
