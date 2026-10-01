package cybersecurity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatusCountsWatermarks(t *testing.T) {
	c := Canary{Directory: t.TempDir()}
	s, err := c.Status()
	if err != nil || s.HostWatermarks != 0 || s.SubWatermarks != 0 {
		t.Fatalf("fresh: %+v %v", s, err)
	}
	if err := c.Configure(true); err != nil {
		t.Fatal(err)
	}
	if s, _ = c.Status(); s.HostWatermarks != 1 || s.SubWatermarks != 0 {
		t.Fatalf("enabled: %+v", s)
	}
	sub := filepath.Join(c.Directory, "security-canary", "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(sub, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Mkdir(filepath.Join(sub, "dir"), 0o700) // folders are not counted
	if s, _ = c.Status(); s.SubWatermarks != 2 {
		t.Fatalf("sub: %+v", s)
	}
	// Turning the monitor off leaves the decoy installed.
	if err := c.Configure(false); err != nil {
		t.Fatal(err)
	}
	if s, _ = c.Status(); s.HostWatermarks != 1 {
		t.Fatalf("disabled: %+v", s)
	}
}
