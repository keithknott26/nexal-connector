package sandbox

import (
	"context"
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
