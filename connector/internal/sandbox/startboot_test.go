package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

// startBoot refuses a task before any provisioning begins: each refusal is
// reported as a failed state with the reason and creates no sandbox record.
func TestStartBootRefusalsReportFailedWithoutRecord(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	valid := func() Task {
		return Task{TaskID: "t1", SandboxID: "sb1", Kind: KindCreate, Hostname: "h",
			SetupKey: "abcdefgh12345678", Devcontainer: &Devcontainer{Template: "go"}, ExpiresAt: &past}
	}
	cases := []struct {
		name string
		task func() Task
		dev  DevOps
		want string
	}{
		{"invalid task", func() Task { tk := valid(); tk.Hostname = "bad host!"; return tk }, &fakeDev{}, "invalid task"},
		{"dev unavailable", valid, nil, "not available"},
		{"already expired", valid, &fakeDev{}, "already expired"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newTestManager(t, &fakeHyp{}, c.dev, nil)
			rc := &recCoord{}
			withCoord(m, rc)
			m.startBoot(context.Background(), c.task())
			reps := rc.all()
			if len(reps) != 1 || reps[0].State != StateFailed || reps[0].SandboxID != "sb1" ||
				!strings.Contains(reps[0].Error, c.want) {
				t.Fatalf("reports = %+v, want one failed containing %q", reps, c.want)
			}
			m.mu.Lock()
			_, exists := m.boxes["sb1"]
			m.mu.Unlock()
			if exists {
				t.Fatal("refused task must not create a record")
			}
		})
	}
}
