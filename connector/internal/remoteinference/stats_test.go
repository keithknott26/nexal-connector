package remoteinference

import (
	"encoding/json"
	"testing"
)

func TestStatsIsNilUntilSomethingIsAnswered(t *testing.T) {
	if NewMeter().Stats() != nil {
		t.Fatal("an idle meter has nothing to report")
	}
	var m *Meter
	if m.Stats() != nil {
		t.Fatal("a nil meter reports nothing")
	}
}

func TestStatsSummarisesMinutesPromptsAndTheLimit(t *testing.T) {
	m := NewMeter()
	m.SetThreshold(6000)
	m.Add(Local, 50)
	m.Done(Local, 0, "")
	m.Add(Offload, 30)
	m.Done(Offload, 0, "RunPod")
	m.Prompt(Local, 800)
	m.Prompt(Offload, 9000)
	st := m.Stats()
	if st == nil {
		t.Fatal("expected stats")
	}
	if st.LocalTokens != 50 || st.OffloadTokens != 30 || st.LocalRequests != 1 || st.OffloadRequests != 1 || st.OffloadName != "RunPod" || st.MaxPromptTokens != 6000 {
		t.Fatalf("totals: %+v", st)
	}
	var lt, ot int64
	for _, mn := range st.Minutes {
		if mn.T%60 != 0 {
			t.Fatalf("minute %d is not on a minute boundary", mn.T)
		}
		lt += mn.LT
		ot += mn.OT
	}
	if lt != 50 || ot != 30 {
		t.Fatalf("per-minute tokens %d/%d, want 50/30", lt, ot)
	}
	if len(st.Prompts) != 2 || st.Prompts[0].P != 800 || st.Prompts[0].O || !st.Prompts[1].O {
		t.Fatalf("prompts: %+v", st.Prompts)
	}
}

func TestStatsStaysWithinWhatTheCoordinatorAccepts(t *testing.T) {
	m := NewMeter()
	for i := 0; i < 100; i++ {
		m.Prompt(Local, 100+i)
	}
	m.Add(Local, 1)
	m.Done(Local, 0, "")
	st := m.Stats()
	if len(st.Prompts) != statsPrompts || len(st.Minutes) > statsMinutes {
		t.Fatalf("%d prompts, %d minutes", len(st.Prompts), len(st.Minutes))
	}
	if b, _ := json.Marshal(st); len(b) > 4096 {
		t.Fatalf("summary is %d bytes; the coordinator stores at most 4096", len(b))
	}
}
