package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A VM whose helper quits (a rejected configuration, a failed start) must say
// why: nexal-vmhost's stderr log is deleted with the box right after.
func TestVMExitCarriesTheHelpersReason(t *testing.T) {
	hv := &fakeHyp{}
	m := newTestManager(t, hv, nil, &fakeGuest{rep: GuestReply{OK: true}})
	id := "7e8f8f8e-6136-450f-b90c-4f471b29974e"
	if err := os.MkdirAll(m.boxDir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	reason := "nexal-vmhost: invalid configuration: The memory size must be a multiple of 1 MB."
	if err := os.WriteFile(m.consoleLog(id)+".host", []byte("starting\n"+reason+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.consoleLog(id), []byte("EFI stub: Booting Linux Kernel...\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	put(m, &record{ID: id, State: StateProvisioning, Kind: SandboxVM})
	_, err := m.awaitFirstBoot(context.Background(), id, Handle{SandboxID: id, Label: "gone"})
	if err == nil || !strings.Contains(err.Error(), "VM exited before first boot completed") ||
		!strings.Contains(err.Error(), reason) || !strings.Contains(err.Error(), "Booting Linux Kernel") {
		t.Fatalf("%v", err)
	}
}
