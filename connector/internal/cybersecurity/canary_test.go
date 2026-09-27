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

func TestCanaryTypedSignalsAndReenablePreservesAlert(t *testing.T) {
	c := Canary{Directory: t.TempDir()}
	ctx := context.Background()
	if err := c.Configure(true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.Directory, "security-canary", canaryName)
	check := func(want string) {
		t.Helper()
		var got string
		if err := c.Tick(ctx, time.Now(), func(_ context.Context, e Event) error { got = e.Detector; return e.Validate(time.Now()) }); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	check("nexal_canary_modified")
	if err := c.Configure(false); err != nil {
		t.Fatal(err)
	}
	if err := c.Configure(true); err != nil {
		t.Fatal(err)
	}
	s, _ := c.Status()
	if s.Status != "alert" {
		t.Fatal("re-enabling concealed changed decoy")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check("nexal_canary_deleted")
	if err := os.Symlink("/not-a-real-target", path); err != nil {
		t.Fatal(err)
	}
	check("nexal_canary_unsafe")
}

func TestCanaryRecoversAfterLongOfflineGap(t *testing.T) {
	c := Canary{Directory: t.TempDir()}
	if err := c.Configure(true); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(c.Directory, "security-canary", canaryName)); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	_ = c.Tick(context.Background(), old, func(context.Context, Event) error { return errors.New("offline") })
	if err := c.Tick(context.Background(), time.Now(), func(_ context.Context, e Event) error {
		if e.Detector != "nexal_canary_deleted" {
			t.Error("lost current missing decoy")
		}
		return e.Validate(time.Now())
	}); err != nil {
		t.Fatal(err)
	}
}
