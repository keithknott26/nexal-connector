package cybersecurity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testOperation(kind string) WatermarkOperation {
	return WatermarkOperation{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", kind, time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)}
}
func TestWatermarkRegenerationIdempotentAndOptIn(t *testing.T) {
	c := Canary{Directory: t.TempDir()}
	ctx := context.Background()
	op := testOperation("regenerate")
	if err := c.ExecuteOperation(ctx, op, Scanner{}, nil); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Status()
	if s.OperationResult.Checks[0].Status != "blocked" {
		t.Fatal("disabled watermark rotated")
	}
	if err := c.Configure(true); err != nil {
		t.Fatal(err)
	}
	old, _ := c.Status()
	op.ID = "bbbbbbbb-bbbb-cccc-dddd-eeeeeeeeeeee"
	if err := c.ExecuteOperation(ctx, op, Scanner{}, nil); err != nil {
		t.Fatal(err)
	}
	fresh, _ := c.Status()
	if fresh.Baseline == old.Baseline || fresh.OperationResult.Checks[0].Status != "passed" {
		t.Fatal("not regenerated")
	}
	if err := c.ExecuteOperation(ctx, op, Scanner{}, nil); err != nil {
		t.Fatal(err)
	}
	replay, _ := c.Status()
	if replay.Baseline != fresh.Baseline {
		t.Fatal("duplicate rotated twice")
	}
	op.ExpiresAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	if c.ExecuteOperation(ctx, op, Scanner{}, nil) == nil {
		t.Fatal("expired command executed")
	}
}
func TestWatermarkRotationRecoversWithoutFalseAlert(t *testing.T) {
	c := Canary{Directory: t.TempDir()}
	if err := c.Configure(true); err != nil {
		t.Fatal(err)
	}
	op := testOperation("regenerate")
	content := []byte("new private synthetic marker")
	err := c.locked(func(root *os.Root, s *CanaryState) error {
		name := "replacement-" + op.ID
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		f.Write(content)
		f.Close()
		s.Rotation = &watermarkRotation{op.ID, name, digest(content)}
		if err := saveCanary(root, s); err != nil {
			return err
		}
		return root.Rename(name, canaryName)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(context.Background(), time.Now(), func(context.Context, Event) error {
		t.Fatal("false integrity alert after authorized rotation")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Status()
	if s.Baseline != digest(content) || s.OperationResult.ID != op.ID {
		t.Fatal("rotation not recovered")
	}
}
func TestWatermarkSelfTestLeavesRealDecoyAndConfigUntouched(t *testing.T) {
	c := Canary{Directory: t.TempDir()}
	if err := c.Configure(true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Directory, "security-canary", canaryName)
	before, _ := os.ReadFile(path)
	if err := c.ExecuteOperation(context.Background(), testOperation("self_test"), Scanner{Directory: c.Directory}, func(_ context.Context, e Event) error {
		if e.Kind != "sensor_health" || e.Severity != "info" {
			t.Fatal("self-test misreported as threat")
		}
		return e.Validate(time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("modified active decoy")
	}
	s, _ := c.Status()
	checks := map[string]string{}
	for _, c := range s.OperationResult.Checks {
		checks[c.Name] = c.Status
	}
	if checks["watermark_integrity"] != "passed" || checks["report_delivery"] != "passed" || checks["scanner_configuration"] != "not_configured" || checks["outbound_sensor"] != "unavailable" {
		t.Fatal(checks)
	}
}
func TestConfiguredScannerSelfTestUsesActualEngine(t *testing.T) {
	engine := testEngine(t)
	s, files := testScanner(t)
	if err := s.Configure(true, []string{files}, engine); err != nil {
		t.Fatal(err)
	}
	checks := s.ConfigurationSelfTest(context.Background())
	for _, c := range checks {
		if c.Status != "passed" {
			t.Fatal(checks)
		}
	}
}
