package lanshare

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"
)

func shPath(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	return "/bin/sh"
}

func TestExecRunnerReturnsTrimmedOutputWithoutShellOrEnvironment(t *testing.T) {
	sh := shPath(t)
	t.Setenv("NEXAL_LANSHARE_LEAK", "inherited")
	out, err := ExecRunner{}.Run(context.Background(), sh, "-c", `printf '  [%s]\n' "${NEXAL_LANSHARE_LEAK-unset}"`)
	if err != nil {
		t.Fatal(err)
	}
	if out != "[unset]" {
		t.Fatalf("output = %q, want trimmed output with an empty environment", out)
	}
}

func TestExecRunnerFailureCarriesBoundedOutput(t *testing.T) {
	sh := shPath(t)
	_, err := ExecRunner{}.Run(context.Background(), sh, "-c", "echo boom; exit 3")
	var ee *exec.ExitError
	if err == nil || !errors.As(err, &ee) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want wrapped exit error carrying command output", err)
	}

	out, err := ExecRunner{}.Run(context.Background(), sh, "-c", "head -c 5000 /dev/zero | tr '\\0' x; exit 1")
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.HasSuffix(out, "… (truncated)") || len(out) > 2048+len("… (truncated)") {
		t.Fatalf("output not truncated: len=%d", len(out))
	}

	out, err = ExecRunner{}.Run(context.Background(), sh, "-c", "exit 2")
	if out != "" || err == nil || !errors.As(err, &ee) {
		t.Fatalf("silent failure: out=%q err=%v", out, err)
	}
}

func TestExecRunnerHonoursContextAndMissingProgram(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (ExecRunner{}).Run(ctx, shPath(t), "-c", "echo hi"); err == nil {
		t.Fatal("cancelled context ran the command")
	}
	if _, err := (ExecRunner{}).Run(context.Background(), "/nonexistent/nexal-tool"); err == nil {
		t.Fatal("missing program accepted")
	}
}

func TestExecRunnerTruncationKeepsValidUTF8(t *testing.T) {
	sh := shPath(t)
	// One leading byte puts a 2-byte rune across the 2048-byte cut.
	out, err := ExecRunner{}.Run(context.Background(), sh, "-c", "printf x; i=0; while [ $i -lt 2000 ]; do printf 'é'; i=$((i+1)); done; exit 1")
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.HasSuffix(out, "… (truncated)") || !utf8.ValidString(out) {
		t.Fatalf("truncated output is not valid UTF-8 (len=%d)", len(out))
	}
}
