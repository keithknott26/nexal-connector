package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type vzCall struct {
	name string
	args []string
}

func newFakeVZ(t *testing.T, out []byte, err error) (*vzHypervisor, *[]vzCall) {
	t.Helper()
	var calls []vzCall
	h := NewVZHypervisor(filepath.Join(t.TempDir(), "nexal-vmhost"), t.TempDir()).(*vzHypervisor)
	h.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, vzCall{name, args})
		return out, err
	}
	return h, &calls
}

func TestVZHypervisorStopRequiresControlSocket(t *testing.T) {
	h, calls := newFakeVZ(t, nil, nil)
	if err := h.Stop(context.Background(), Handle{}); err == nil || !strings.Contains(err.Error(), "no control socket") {
		t.Fatalf("empty control: %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("unexpected commands: %v", *calls)
	}
	if err := h.Stop(context.Background(), Handle{Control: "/c.sock"}); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.name != h.vmhost || strings.Join(c.args, " ") != "stop --control /c.sock" {
		t.Fatalf("call = %+v", c)
	}
	h, _ = newFakeVZ(t, []byte("boom"), errors.New("exit 1"))
	if err := h.Stop(context.Background(), Handle{Control: "/c.sock"}); err == nil || !strings.Contains(err.Error(), "stop request failed") {
		t.Fatalf("failure: %v", err)
	}
}

func TestVZHypervisorAlive(t *testing.T) {
	ctx := context.Background()
	h, calls := newFakeVZ(t, nil, nil)
	if alive, err := h.Alive(ctx, Handle{}); alive || err != nil || len(*calls) != 0 {
		t.Fatalf("empty label: %v %v %v", alive, err, *calls)
	}
	h, calls = newFakeVZ(t, []byte("state = running\n"), nil)
	if alive, err := h.Alive(ctx, Handle{Label: "l1"}); !alive || err != nil {
		t.Fatalf("running: %v %v", alive, err)
	}
	if got := (*calls)[0].args; got[0] != "print" || got[1] != h.domain()+"/l1" {
		t.Fatalf("args = %v", got)
	}
	h, _ = newFakeVZ(t, []byte("state = waiting\n"), nil)
	if alive, _ := h.Alive(ctx, Handle{Label: "l1"}); alive {
		t.Fatal("waiting must not be alive")
	}
	h, _ = newFakeVZ(t, nil, errors.New("exit 113"))
	if alive, err := h.Alive(ctx, Handle{Label: "l1"}); alive || err != nil {
		t.Fatalf("not loaded: %v %v", alive, err)
	}
}

func TestVZHypervisorKillCleansUpFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mk := func(n string) string {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	h, calls := newFakeVZ(t, nil, errors.New("exit 113")) // bootout and print both fail: job already gone
	if err := h.Kill(ctx, Handle{}); err != nil || len(*calls) != 0 {
		t.Fatalf("empty label: %v %v", err, *calls)
	}
	hd := Handle{Label: "l1", Plist: mk("p"), Spec: mk("s"), Control: mk("c"), Guest: mk("g")}
	if err := h.Kill(ctx, hd); err != nil {
		t.Fatalf("already unloaded must succeed: %v", err)
	}
	for _, p := range []string{hd.Plist, hd.Spec, hd.Control, hd.Guest} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s not removed", p)
		}
	}
}
