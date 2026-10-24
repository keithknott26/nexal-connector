package cybersecurity

import (
	"context"
	"path/filepath"
	"testing"
)

func TestForConfigUsesConfigDirectory(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	want := filepath.Dir(cfg)
	if got := CanaryForConfig(cfg).Directory; got != want {
		t.Fatalf("canary directory = %q, want %q", got, want)
	}
	if got := HoneypotForConfig(cfg).Directory; got != want {
		t.Fatalf("honeypot directory = %q, want %q", got, want)
	}
}

func TestMarkOperationReportedMatchesOnlyCurrentResult(t *testing.T) {
	c := Canary{Directory: t.TempDir()}
	op := testOperation("regenerate")
	if err := c.ExecuteOperation(context.Background(), op, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkOperationReported("some-other-id"); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Status(); s.OperationReported {
		t.Fatal("unrelated id marked the operation reported")
	}
	if err := c.MarkOperationReported(op.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := c.Status(); !s.OperationReported {
		t.Fatal("matching id did not mark the operation reported")
	}
}
