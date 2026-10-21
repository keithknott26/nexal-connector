package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/cybersecurity"
)

func buildBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "normalize")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func run(t *testing.T, bin, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

func TestCLI(t *testing.T) {
	bin := buildBin(t)
	ts := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	alert := `{"event_type":"alert","timestamp":"` + ts + `","alert":{"signature_id":2001,"severity":1}}`
	flags := []string{"-source-id", "capture_1", "-rules-version", "public_1"}

	t.Run("missing flags", func(t *testing.T) {
		if _, _, code := run(t, bin, ""); code != 2 {
			t.Fatalf("exit %d, want 2", code)
		}
	})
	t.Run("alerts emitted and non-alerts skipped", func(t *testing.T) {
		out, _, code := run(t, bin, `{"event_type":"dns"}`+"\n"+alert+"\n", flags...)
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
		if n := strings.Count(out, "\n"); n != 1 || !strings.Contains(out, "suricata_2001") {
			t.Fatalf("unexpected output %q", out)
		}
	})
	t.Run("invalid line reports line number", func(t *testing.T) {
		_, errOut, code := run(t, bin, alert+"\nnot json\n", flags...)
		if code != 1 || !strings.Contains(errOut, "line 2") {
			t.Fatalf("exit %d stderr %q", code, errOut)
		}
	})
	t.Run("oversized line is rejected", func(t *testing.T) {
		big := strings.Repeat("x", cybersecurity.MaxEVEBytes+2)
		_, errOut, code := run(t, bin, big+"\n", flags...)
		if code != 1 || !strings.Contains(errOut, "line limit exceeded") {
			t.Fatalf("exit %d stderr %q", code, errOut)
		}
	})
}
