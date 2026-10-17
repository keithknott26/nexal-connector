package inference

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupRejectsOtherPlatforms(t *testing.T) {
	var got []any
	s := &RuntimeSetup{InferenceDir: t.TempDir(), GOOS: "linux", GOARCH: "amd64", Emit: func(ev any) { got = append(got, ev) }}
	err := s.Setup(context.Background())
	if AsError(err).Code != "unsupported_platform" || len(got) != 1 {
		t.Fatalf("err=%v events=%v", err, got)
	}
}

func fakeRun(dir string, fail string) (Commander, *[]string) {
	var calls []string
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if fail != "" && strings.Contains(strings.Join(args, " "), fail) {
			return []byte("boom"), errors.New("exit 1")
		}
		if len(args) >= 2 && args[0] == "-m" && args[1] == "venv" {
			py := filepath.Join(args[2], "bin", "python3")
			_ = os.MkdirAll(filepath.Dir(py), 0o755)
			_ = os.WriteFile(py, []byte("#!/bin/sh\n"), 0o755)
		}
		return nil, nil
	}, &calls
}

func TestSetupInstallsPinnedPackagesBinaryOnly(t *testing.T) {
	dir := t.TempDir()
	py := filepath.Join(dir, "python3")
	_ = os.WriteFile(py, []byte("x"), 0o755)
	run, calls := fakeRun(dir, "")
	s := &RuntimeSetup{InferenceDir: filepath.Join(dir, "inference"), GOOS: "darwin", GOARCH: "arm64", Run: run, PythonCandidates: []string{py}}
	if err := s.Setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(*calls, "\n")
	for _, want := range []string{"--only-binary=:all:", "mlx==" + RuntimeMLXVersion, "mlx-lm==" + RuntimeMLXLMVersion, "transformers==" + RuntimeTransformersVersion} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in\n%s", want, all)
		}
	}
	if !CheckRuntime(s.InferenceDir).Ready {
		t.Fatal("not ready after setup")
	}
	// Second run only confirms the imports.
	run2, calls2 := fakeRun(dir, "")
	s.Run = run2
	if err := s.Setup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*calls2) != 1 {
		t.Fatalf("idempotent run made %d calls: %v", len(*calls2), *calls2)
	}
}

func TestSetupReportsPipFailure(t *testing.T) {
	dir := t.TempDir()
	py := filepath.Join(dir, "python3")
	_ = os.WriteFile(py, []byte("x"), 0o755)
	run, _ := fakeRun(dir, "pip install")
	s := &RuntimeSetup{InferenceDir: filepath.Join(dir, "inference"), GOOS: "darwin", GOARCH: "arm64", Run: run, PythonCandidates: []string{py}}
	if AsError(s.Setup(context.Background())).Code != "pip_failed" {
		t.Fatal("expected pip_failed")
	}
	if CheckRuntime(s.InferenceDir).Ready {
		t.Fatal("ready after a failed install")
	}
}

func TestSetupNeedsPython(t *testing.T) {
	s := &RuntimeSetup{InferenceDir: t.TempDir(), GOOS: "darwin", GOARCH: "arm64", PythonCandidates: []string{"/nonexistent/python3"}}
	if AsError(s.Setup(context.Background())).Code != "python_missing" {
		t.Fatal("expected python_missing")
	}
}

func TestServeCommandValidation(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, err := ServeCommand(dir, "qwen3-4b-q4", "100.64.0.1", 8080); AsError(err).Code != "runtime_not_set_up" {
		t.Fatalf("got %v", err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "venv", "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "venv", "bin", "python3"), []byte("x"), 0o755)
	_ = os.WriteFile(runtimeMarker(dir), []byte(`{"MLX":"`+RuntimeMLXVersion+`","MLXLM":"`+RuntimeMLXLMVersion+`","Transformers":"`+RuntimeTransformersVersion+`"}`), 0o600)
	for _, c := range []struct {
		id, host string
		port     int
		code     string
	}{
		{"../x", "100.64.0.1", 8080, "invalid_arguments"},
		{"qwen3-4b-q4", "0.0.0.0", 8080, "invalid_arguments"},
		{"qwen3-4b-q4", "", 8080, "invalid_arguments"},
		{"qwen3-4b-q4", "100.64.0.1", 80, "invalid_arguments"},
		{"qwen3-4b-q4", "100.64.0.1", 8080, "not_installed"},
	} {
		if _, _, _, err := ServeCommand(dir, c.id, c.host, c.port); AsError(err).Code != c.code {
			t.Fatalf("%+v: got %v", c, err)
		}
	}
	_ = os.MkdirAll(ModelDir(dir, "qwen3-4b-q4"), 0o755)
	name, args, env, err := ServeCommand(dir, "qwen3-4b-q4", "100.64.0.1", 8080)
	if err != nil || name != VenvPython(dir) {
		t.Fatalf("%v %s", err, name)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-I -m mlx_lm.server") || !strings.Contains(joined, "--host 100.64.0.1") || strings.Contains(joined, "trust-remote-code") {
		t.Fatalf("args: %s", joined)
	}
	if !strings.Contains(strings.Join(env, " "), "HF_HUB_OFFLINE=1") {
		t.Fatal("hugging face must be offline while serving")
	}
}
