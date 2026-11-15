package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// NewVZHypervisorWithSockets must keep the socket directory separate from the
// run directory and search for nexal-vmhost under the data dir derived from
// runDir (<base>/sandboxes/run).
func TestNewVZHypervisorWithSocketsWiring(t *testing.T) {
	base := t.TempDir()
	runDir := filepath.Join(base, "sandboxes", "run")
	sockDir := filepath.Join(t.TempDir(), "s")
	h, ok := NewVZHypervisorWithSockets("/x/nexal-vmhost", runDir, sockDir).(*vzHypervisor)
	if !ok {
		t.Fatal("expected *vzHypervisor")
	}
	if h.vmhost != "/x/nexal-vmhost" || h.runDir != runDir || h.sockDir != sockDir {
		t.Fatalf("unexpected fields: %+v", h)
	}
	if h.uid != os.Getuid() || h.run == nil || h.search == nil {
		t.Fatalf("uid/run/search not initialised: %+v", h)
	}
	want := filepath.Join(base, "bin", "nexal-vmhost")
	found := false
	for _, c := range h.search() {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("search() missing %s: %v", want, h.search())
	}
}
