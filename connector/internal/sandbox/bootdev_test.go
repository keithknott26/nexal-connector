package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A dev task without a devcontainer payload must fail before touching the
// DevOps backend (nil here, so reaching Up would panic).
func TestBootDevRejectsMissingDevcontainer(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	err := m.bootDev(context.Background(), Task{SandboxID: "sb-dev"}, false)
	if err == nil || !strings.Contains(err.Error(), "devcontainer payload missing") {
		t.Fatalf("err = %v, want devcontainer payload missing", err)
	}
}

type scriptedDev struct {
	fakeDev
	res DevUpResult
	err error
}

func (s *scriptedDev) Up(ctx context.Context, spec DevUpSpec) (DevUpResult, error) {
	s.mu.Lock()
	s.ups = append(s.ups, spec)
	s.mu.Unlock()
	return s.res, s.err
}

func devTask() Task {
	return Task{SandboxID: "sb-dev", Hostname: "h", Devcontainer: &Devcontainer{Template: "go"}}
}

// A successful Up moves the record to running and records the mesh address,
// host key and workspace the DevOps backend returned.
func TestBootDevSuccessRecordsRunningState(t *testing.T) {
	dev := &scriptedDev{res: DevUpResult{MeshIP: "100.64.0.9", HostKeyFingerprint: "SHA256:abc"}}
	m := newTestManager(t, &fakeHyp{}, dev, nil)
	put(m, &record{ID: "sb-dev", State: StateProvisioning, Kind: SandboxDevcontainer, AwaitKey: true})
	if err := m.bootDev(context.Background(), devTask(), false); err != nil {
		t.Fatal(err)
	}
	r := snap(m, "sb-dev")
	if r.State != StateRunning || r.MeshIP != "100.64.0.9" || r.HostKey != "SHA256:abc" || r.AwaitKey {
		t.Fatalf("%+v", r)
	}
	if r.Workspace != DevWorkspaceID("sb-dev") || len(dev.ups) != 1 || dev.ups[0].Workspace != r.Workspace {
		t.Fatalf("workspace %q ups %+v", r.Workspace, dev.ups)
	}
}

// A failing Up is returned unchanged and leaves the record untouched.
func TestBootDevPropagatesUpFailure(t *testing.T) {
	dev := &scriptedDev{err: errors.New("docker down")}
	m := newTestManager(t, &fakeHyp{}, dev, nil)
	put(m, &record{ID: "sb-dev", State: StateProvisioning, Kind: SandboxDevcontainer})
	err := m.bootDev(context.Background(), devTask(), false)
	if err == nil || err.Error() != "docker down" {
		t.Fatalf("err = %v", err)
	}
	if r := snap(m, "sb-dev"); r.State != StateProvisioning || r.Workspace != "" {
		t.Fatalf("%+v", r)
	}
}

// If the sandbox record is deleted while Up runs, boot reports it rather than
// resurrecting state.
func TestBootDevSandboxVanishedDuringUp(t *testing.T) {
	m := newTestManager(t, &fakeHyp{}, &scriptedDev{}, nil)
	err := m.bootDev(context.Background(), devTask(), false)
	if err == nil || !strings.Contains(err.Error(), "vanished") {
		t.Fatalf("err = %v", err)
	}
}
