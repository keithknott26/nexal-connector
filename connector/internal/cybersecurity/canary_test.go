package cybersecurity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCanaryLifecycleAndDurableRetry(t *testing.T) {
	c := Canary{Directory: t.TempDir()}
	now := time.Now()
	ctx := context.Background()
	if err := c.Configure(true); err != nil {
		t.Fatal(err)
	}
	count := 0
	var first Event
	report := func(_ context.Context, e Event) error { count++; first = e; return errors.New("offline") }
	if err := c.Tick(ctx, now, report); err != nil || count != 0 {
		t.Fatal("unchanged canary alerted", err, count)
	}
	decoy := filepath.Join(c.Directory, "security-canary", canaryName)
	if err := os.WriteFile(decoy, []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	if c.Tick(ctx, now, report) == nil || count != 1 {
		t.Fatal("changed canary was not retained")
	}
	reloaded := Canary{Directory: c.Directory}
	if err := reloaded.Tick(ctx, now.Add(time.Minute), func(_ context.Context, e Event) error {
		count++
		if e != first {
			t.Fatal("retry altered persisted event")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(ctx, now.Add(2*time.Minute), report); err != nil || count != 2 {
		t.Fatal("unchanged alert repeated", err, count)
	}
	if err := c.Configure(false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(decoy); err != nil {
		t.Fatal(err)
	}
	if err := c.Tick(ctx, now, report); err != nil || count != 2 {
		t.Fatal("disabled monitor sent alert")
	}
	if err := c.Configure(true); err != nil {
		t.Fatal(err)
	}
	if c.Tick(ctx, now, report) == nil || count != 3 {
		t.Fatal("missing decoy not detected")
	}
}
func TestCanaryRejectsDirectorySymlinkAndDetectsDecoySymlink(t *testing.T) {
	c := Canary{Directory: t.TempDir()}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(c.Directory, "security-canary")); err != nil {
		t.Fatal(err)
	}
	if c.Configure(true) == nil {
		t.Fatal("followed state directory symlink")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("modified outside directory")
	}
	os.Remove(filepath.Join(c.Directory, "security-canary"))
	if err := c.Configure(true); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(c.Directory, "security-canary", canaryName)
	os.Remove(decoy)
	if err := os.Symlink(filepath.Join(outside, "secret"), decoy); err != nil {
		t.Fatal(err)
	}
	reported := false
	if err := c.Tick(context.Background(), time.Now(), func(_ context.Context, e Event) error { reported = true; return nil }); err != nil || !reported {
		t.Fatal("symlink not detected", err)
	}
}
