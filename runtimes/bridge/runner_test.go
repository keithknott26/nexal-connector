//go:build darwin || linux

package runtimebridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fixture struct {
	owner  OwnerPolicy
	job    LocalJob
	dir    string
	result InferenceResult
	grant  admission
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func put(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func putJSON(t *testing.T, path string, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, data, 0600)
	return data
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "release")
	if err := os.MkdirAll(filepath.Join(root, "nexal_mlx"), 0700); err != nil {
		t.Fatal(err)
	}
	owner := OwnerPolicy{
		Installation: LocalInstallation{
			Python: filepath.Join(dir, "python"),
			Entry:  filepath.Join(root, "nexal_mlx_entry.py"),
			Config: filepath.Join(dir, "config.json"),
		},
		RuntimeFilesSHA256: map[string]string{},
	}
	// This file is never executed. Only the injected compiled Go helper runs.
	python := []byte("reviewed Python placeholder; tests do not import MLX\n")
	put(t, owner.Installation.Python, python, 0700)
	owner.PythonSHA256 = hash(python)
	for _, name := range []string{"nexal_mlx_entry.py", "nexal_mlx/__init__.py",
		"nexal_mlx/cli.py", "nexal_mlx/runtime.py", "nexal_mlx/security.py", "nexal_mlx/ring.py"} {
		data := []byte("# reviewed fixture " + name + "\n")
		put(t, filepath.Join(root, name), data, 0600)
		owner.RuntimeFilesSHA256[name] = hash(data)
	}
	owner.EntrySHA256 = owner.RuntimeFilesSHA256["nexal_mlx_entry.py"]
	model := strings.Repeat("a", 64)
	config := localConfig{1, filepath.Join(dir, "model"), model, filepath.Join(dir, "receipt.json"), strings.Repeat("b", 64)}
	owner.ConfigSHA256 = hash(putJSON(t, owner.Installation.Config, config))
	job := LocalJob{AdmissionPath: filepath.Join(dir, "admission.json"),
		PromptPath: filepath.Join(dir, "prompt;$(no-shell).txt"), AttemptID: "attempt_001",
		ModelManifestSHA256: model, MaxTokens: 128, Timeout: 3 * time.Second}
	grant := admission{1, job.AttemptID, model, 4096, 8192, 1024, time.Now().Unix(), time.Now().Add(120 * time.Second).Unix()}
	putJSON(t, job.AdmissionPath, grant)
	put(t, job.PromptPath, []byte("private prompt fixture"), 0600)
	result := InferenceResult{1, "mlx-local-text-v1", "0.1.0", job.AttemptID, model, "hello", 2048}
	putJSON(t, filepath.Join(dir, "result.json"), result)
	return fixture{owner, job, dir, result, grant}
}

func (f fixture) factory(t *testing.T, mode string) commandFactory {
	t.Helper()
	return func(ctx context.Context, command Command) *exec.Cmd {
		want, err := InferenceCommand(f.owner.Installation, f.job.AdmissionPath, f.job.PromptPath, f.job.MaxTokens)
		if err != nil || !reflect.DeepEqual(command, want) {
			t.Fatalf("supervisor did not use fixed InferenceCommand: %#v", command)
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		return exec.CommandContext(ctx, exe, "-test.run=^TestMLXHelperProcess$", "--", "--mlx-helper", mode, f.dir)
	}
}

// TestMLXHelperProcess is an isolated compiled helper, never a shell or Python.
func TestMLXHelperProcess(t *testing.T) {
	at := -1
	for i, arg := range os.Args {
		if arg == "--mlx-helper" {
			at = i
		}
	}
	if at < 0 {
		return
	}
	mode, dir := os.Args[at+1], os.Args[at+2]
	_ = os.WriteFile(filepath.Join(dir, "started"), []byte(strconv.Itoa(os.Getpid())), 0600)
	switch mode {
	case "success":
		data, _ := os.ReadFile(filepath.Join(dir, "result.json"))
		_, _ = os.Stdout.Write(data)
	case "failure":
		fmt.Fprint(os.Stderr, "SECRET_PROMPT /private/owner/token\n")
		fmt.Fprint(os.Stdout, "PRIVATE_OUTPUT")
		os.Exit(9)
	case "sleep":
		time.Sleep(30 * time.Second)
	case "stdout":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("s"), 1024*1024))
		time.Sleep(30 * time.Second)
	case "stderr":
		_, _ = os.Stderr.Write(bytes.Repeat([]byte("e"), 1024*1024))
		time.Sleep(30 * time.Second)
	case "environment":
		if os.Getenv("SECRET_PARENT_KEY") != "" || os.Getenv("PYTHONPATH") != "" ||
			os.Getenv("DYLD_INSERT_LIBRARIES") != "" || os.Getenv("HTTP_PROXY") != "" ||
			os.Getenv("MLXLM_USE_MODELSCOPE") != "" || os.Getenv("HF_HUB_OFFLINE") != "1" ||
			os.Getenv("TRANSFORMERS_OFFLINE") != "1" {
			os.Exit(8)
		}
		data, _ := os.ReadFile(filepath.Join(dir, "result.json"))
		_, _ = os.Stdout.Write(data)
	case "descendant":
		time.Sleep(30 * time.Second)
	case "child", "orphan":
		exe, _ := os.Executable()
		child := exec.Command(exe, "-test.run=^TestMLXHelperProcess$", "--", "--mlx-helper", "descendant", dir)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(7)
		}
		_ = os.WriteFile(filepath.Join(dir, "child.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0600)
		if mode == "child" {
			_ = child.Wait()
		} else {
			data, _ := os.ReadFile(filepath.Join(dir, "result.json"))
			_, _ = os.Stdout.Write(data)
		}
	default:
		os.Exit(6)
	}
	os.Exit(0)
}

func TestSupervisedSuccess(t *testing.T) {
	f := newFixture(t)
	result, err := runLocalInference(context.Background(), f.owner, f.job, f.factory(t, "success"))
	if err != nil || result != f.result {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestPublicPlatformGate(t *testing.T) {
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		t.Skip("native public platform is permitted; no unreviewed MLX fixture executed")
	}
	_, err := RunLocalInference(context.Background(), OwnerPolicy{}, LocalJob{})
	if !errors.Is(err, ErrPlatform) {
		t.Fatalf("native gate: %v", err)
	}
}

func TestEnvironmentIsNotInherited(t *testing.T) {
	for _, key := range []string{"SECRET_PARENT_KEY", "PYTHONPATH", "DYLD_INSERT_LIBRARIES", "HTTP_PROXY", "MLXLM_USE_MODELSCOPE"} {
		t.Setenv(key, "SECRET")
	}
	f := newFixture(t)
	_, err := runLocalInference(context.Background(), f.owner, f.job, f.factory(t, "environment"))
	if err != nil {
		t.Fatal(err)
	}
}

func TestProcessFailureIsSanitized(t *testing.T) {
	f := newFixture(t)
	result, err := runLocalInference(context.Background(), f.owner, f.job, f.factory(t, "failure"))
	if !errors.Is(err, ErrProcess) || result != (InferenceResult{}) ||
		strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "/private") {
		t.Fatalf("unsafe error/result: %#v %v", result, err)
	}
	factory := func(ctx context.Context, _ Command) *exec.Cmd {
		return exec.CommandContext(ctx, filepath.Join(f.dir, "SECRET_MISSING_BINARY"))
	}
	_, err = runLocalInference(context.Background(), f.owner, f.job, factory)
	if err != ErrProcess {
		t.Fatalf("start failure leaked: %v", err)
	}
}

func TestDeadlineAndCancellation(t *testing.T) {
	for _, mode := range []string{"job-deadline", "context-deadline", "cancel", "pre-cancel", "admission-deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			var cancel context.CancelFunc
			want := context.DeadlineExceeded
			switch mode {
			case "job-deadline":
				f.job.Timeout = 120 * time.Millisecond
			case "context-deadline":
				ctx, cancel = context.WithTimeout(ctx, 120*time.Millisecond)
			case "cancel":
				ctx, cancel = context.WithCancel(ctx)
				go func() {
					for i := 0; i < 100; i++ {
						if _, err := os.Stat(filepath.Join(f.dir, "started")); err == nil {
							break
						}
						time.Sleep(10 * time.Millisecond)
					}
					cancel()
				}()
				want = context.Canceled
			case "pre-cancel":
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			case "admission-deadline":
				f.grant.ExpiresAtUnix = time.Now().Add(2 * time.Second).Unix()
				putJSON(t, f.job.AdmissionPath, f.grant)
			}
			if cancel != nil {
				defer cancel()
			}
			start := time.Now()
			result, err := runLocalInference(ctx, f.owner, f.job, f.factory(t, "sleep"))
			if !errors.Is(err, want) || result != (InferenceResult{}) || time.Since(start) > 3*time.Second {
				t.Fatalf("err=%v elapsed=%v result=%#v", err, time.Since(start), result)
			}
			if mode == "pre-cancel" {
				if _, err := os.Stat(filepath.Join(f.dir, "started")); !os.IsNotExist(err) {
					t.Fatal("started a canceled job")
				}
			}
		})
	}
}

func TestOutputLimitsKillProcess(t *testing.T) {
	for _, mode := range []string{"stdout", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.job.StdoutBytes, f.job.StderrBytes = 512, 512
			start := time.Now()
			result, err := runLocalInference(context.Background(), f.owner, f.job, f.factory(t, mode))
			if err != ErrOutputLimit || result != (InferenceResult{}) || time.Since(start) > 2*time.Second {
				t.Fatalf("err=%v elapsed=%v result=%#v", err, time.Since(start), result)
			}
		})
	}
}

func TestBoundedWriterExactBoundary(t *testing.T) {
	calls := 0
	w := &boundedOutput{limit: 4, retain: true, cancel: func() { calls++ }}
	_, _ = w.Write([]byte("1234"))
	if _, exceeded := w.snapshot(); exceeded {
		t.Fatal("exact ceiling rejected")
	}
	_, _ = w.Write([]byte("567890"))
	_, _ = w.Write([]byte("more"))
	data, exceeded := w.snapshot()
	if string(data) != "1234" || !exceeded || calls != 1 || len(w.data) != 4 {
		t.Fatalf("unbounded writer: %q %v %d", data, exceeded, calls)
	}
	discard := &boundedOutput{limit: 4, cancel: func() {}}
	_, _ = discard.Write([]byte("sensitive"))
	if len(discard.data) != 0 {
		t.Fatal("stderr retained")
	}
}

func TestExclusiveConfigLockAndRelease(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runLocalInference(ctx, f.owner, f.job, f.factory(t, "sleep"))
		done <- err
	}()
	started := false
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(filepath.Join(f.dir, "started")); err == nil {
			started = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !started {
		cancel()
		t.Fatal("first invocation did not start")
	}
	// Separate opens of the same inode must conflict, including within one
	// supervisor process. No second child may reach the command factory.
	called := false
	factory := func(context.Context, Command) *exec.Cmd {
		called = true
		panic("competing execution must not launch")
	}
	start := time.Now()
	if _, err := runLocalInference(context.Background(), f.owner, f.job, factory); err != ErrBusy ||
		called || time.Since(start) > time.Second {
		t.Fatalf("competing invocation: err=%v called=%v", err, called)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("first invocation cancellation: %v", err)
	}
	// Cancellation, process failure, and success must all release the descriptor.
	for _, mode := range []string{"failure", "success", "success"} {
		_, err := runLocalInference(context.Background(), f.owner, f.job, f.factory(t, mode))
		if (mode == "failure" && err != ErrProcess) || (mode == "success" && err != nil) {
			t.Fatalf("lock leaked after cleanup, mode=%s err=%v", mode, err)
		}
	}
}

func TestProcessGroupTeardown(t *testing.T) {
	for _, mode := range []string{"child", "orphan"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := runLocalInference(ctx, f.owner, f.job, f.factory(t, mode))
				done <- err
			}()
			pid := 0
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
				if data, err := os.ReadFile(filepath.Join(f.dir, "child.pid")); err == nil {
					pid, _ = strconv.Atoi(string(data))
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid == 0 {
				cancel()
				t.Fatal("helper child never started")
			}
			if mode == "child" {
				cancel()
			}
			select {
			case err := <-done:
				want := error(context.Canceled)
				if mode == "orphan" {
					want = ErrProcess // inherited pipes cause WaitDelay to expire
				}
				if err != want {
					t.Fatalf("unexpected group result: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("supervisor hung on descendant pipes")
			}
			// The ASSERTION is that the descendant does not survive group
			// cancellation. The deadline is only how long we are willing to wait for
			// the kernel to get round to it, and one second was too short: under
			// -race on a loaded CI runner this failed roughly half the time while
			// the teardown itself was working correctly. A flaky gate here is not a
			// cosmetic problem -- the Go job blocks the mac job, so this test
			// randomly blocked every Swift check and every release.
			//
			// Waiting longer does not weaken the property. A descendant that truly
			// survives cancellation still fails, because it is still running at the
			// end of the wait however long the wait is; only the scheduling noise is
			// removed. The loop still exits as soon as the process is gone, so the
			// healthy path costs the same few milliseconds it always did.
			for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline) && processRunning(pid); {
				time.Sleep(10 * time.Millisecond)
			}
			if processRunning(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatal("descendant survived group cancellation")
			}
		})
	}
}

func processRunning(pid int) bool {
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return false
	}
	if runtime.GOOS == "linux" {
		// Containers may have a PID 1 that does not reap orphan zombies.
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err == nil {
			closeParen := bytes.LastIndexByte(data, ')')
			if closeParen >= 0 && len(data) > closeParen+2 && data[closeParen+2] == 'Z' {
				return false
			}
		}
	}
	return true
}

func TestInvalidResults(t *testing.T) {
	cases := map[string]func([]byte) []byte{
		"malformed": func([]byte) []byte { return []byte("{secret") },
		"array":     func([]byte) []byte { return []byte("[]") },
		"empty":     func([]byte) []byte { return nil },
		"trailing":  func(b []byte) []byte { return append(b, []byte("{}")...) },
		"duplicate": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)
		},
		"schema": func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1)
		},
		"null":    func(b []byte) []byte { return bytes.Replace(b, []byte(`"text":"hello"`), []byte(`"text":null`), 1) },
		"unknown": func(b []byte) []byte { return append([]byte(`{"secret":true,`), b[1:]...) },
		"attempt": func(b []byte) []byte { return bytes.Replace(b, []byte("attempt_001"), []byte("other_attempt"), 1) },
		"model": func(b []byte) []byte {
			return bytes.ReplaceAll(b, []byte(strings.Repeat("a", 64)), []byte(strings.Repeat("b", 64)))
		},
		"template": func(b []byte) []byte {
			return bytes.ReplaceAll(b, []byte("mlx-local-text-v1"), []byte("remote-execution"))
		},
		"runtime":  func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("0.1.0"), []byte("0.2.0")) },
		"memory":   func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("2048"), []byte("999999")) },
		"negative": func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("2048"), []byte("-1")) },
		"fraction": func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("2048"), []byte("1.5")) },
		"utf8":     func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("hello"), []byte{0xff}) },
		"missing":  func(b []byte) []byte { return bytes.ReplaceAll(b, []byte(`"text":"hello",`), nil) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			data, _ := json.Marshal(f.result)
			put(t, filepath.Join(f.dir, "result.json"), mutate(data), 0600)
			result, err := runLocalInference(context.Background(), f.owner, f.job, f.factory(t, "success"))
			if err != ErrResult || result != (InferenceResult{}) {
				t.Fatalf("invalid result accepted or leaked: %#v %v", result, err)
			}
		})
	}
}

func TestPolicyAndInputRejectionsBeforeLaunch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fixture)
		want   error
	}{
		{"missing-entry-pin", func(f *fixture) { f.owner.EntrySHA256 = "" }, ErrPolicy},
		{"wrong-entry-pin", func(f *fixture) { f.owner.EntrySHA256 = strings.Repeat("f", 64) }, ErrPolicy},
		{"missing-python-pin", func(f *fixture) { f.owner.PythonSHA256 = "" }, ErrPolicy},
		{"changed-python", func(f *fixture) { put(t, f.owner.Installation.Python, []byte("changed"), 0700) }, ErrPolicy},
		{"missing-config-pin", func(f *fixture) { f.owner.ConfigSHA256 = "" }, ErrPolicy},
		{"changed-config", func(f *fixture) { put(t, f.owner.Installation.Config, []byte("{}"), 0600) }, ErrPolicy},
		{"missing-runtime-pin", func(f *fixture) { delete(f.owner.RuntimeFilesSHA256, "nexal_mlx/runtime.py") }, ErrPolicy},
		{"changed-runtime", func(f *fixture) {
			put(t, filepath.Join(filepath.Dir(f.owner.Installation.Entry), "nexal_mlx/runtime.py"), []byte("# changed"), 0600)
		}, ErrPolicy},
		{"extra-runtime", func(f *fixture) {
			put(t, filepath.Join(filepath.Dir(f.owner.Installation.Entry), "evil.py"), []byte("# evil"), 0600)
		}, ErrPolicy},
		{"extra-bytecode-dir", func(f *fixture) {
			_ = os.Mkdir(filepath.Join(filepath.Dir(f.owner.Installation.Entry), "nexal_mlx/__pycache__"), 0700)
		}, ErrPolicy},
		{"unknown-runtime-pin", func(f *fixture) { f.owner.RuntimeFilesSHA256["missing.py"] = strings.Repeat("a", 64) }, ErrPolicy},
		{"wrong-config-mode", func(f *fixture) { _ = os.Chmod(f.owner.Installation.Config, 0644) }, ErrPolicy},
		{"writable-source", func(f *fixture) { _ = os.Chmod(f.owner.Installation.Entry, 0666) }, ErrPolicy},
		{"relative-python", func(f *fixture) { f.owner.Installation.Python = "python3" }, ErrInput},
		{"missing-admission", func(f *fixture) { f.job.AdmissionPath = filepath.Join(f.dir, "absent.json") }, ErrAdmission},
		{"expired-admission", func(f *fixture) {
			f.grant.ExpiresAtUnix = time.Now().Add(-time.Second).Unix()
			putJSON(t, f.job.AdmissionPath, f.grant)
		}, ErrAdmission},
		{"stale-admission", func(f *fixture) {
			f.grant.ObservedAtUnix = time.Now().Add(-16 * time.Second).Unix()
			putJSON(t, f.job.AdmissionPath, f.grant)
		}, ErrAdmission},
		{"future-admission", func(f *fixture) {
			f.grant.ObservedAtUnix = time.Now().Add(time.Minute).Unix()
			putJSON(t, f.job.AdmissionPath, f.grant)
		}, ErrAdmission},
		{"overlong-lease", func(f *fixture) {
			f.grant.ExpiresAtUnix = time.Now().Add(301 * time.Second).Unix()
			putJSON(t, f.job.AdmissionPath, f.grant)
		}, ErrAdmission},
		{"identity-mismatch", func(f *fixture) { f.job.AttemptID = "wrong" }, ErrAdmission},
		{"model-mismatch", func(f *fixture) { f.job.ModelManifestSHA256 = strings.Repeat("b", 64) }, ErrAdmission},
		{"no-reservation", func(f *fixture) { f.grant.ReservedBytes = 0; putJSON(t, f.job.AdmissionPath, f.grant) }, ErrAdmission},
		{"private-admission", func(f *fixture) { _ = os.Chmod(f.job.AdmissionPath, 0644) }, ErrAdmission},
		{"null-admission", func(f *fixture) { put(t, f.job.AdmissionPath, []byte("null"), 0600) }, ErrAdmission},
		{"blank-prompt", func(f *fixture) { put(t, f.job.PromptPath, []byte(" \n"), 0600) }, ErrInput},
		{"large-prompt", func(f *fixture) { put(t, f.job.PromptPath, bytes.Repeat([]byte("x"), 16*1024+1), 0600) }, ErrInput},
		{"binary-prompt", func(f *fixture) { put(t, f.job.PromptPath, []byte{0xff}, 0600) }, ErrInput},
		{"symlink-prompt", func(f *fixture) {
			path := filepath.Join(f.dir, "prompt-link")
			_ = os.Symlink(f.job.PromptPath, path)
			f.job.PromptPath = path
		}, ErrInput},
		{"fifo-prompt", func(f *fixture) {
			f.job.PromptPath = filepath.Join(f.dir, "fifo")
			_ = syscall.Mkfifo(f.job.PromptPath, 0600)
		}, ErrInput},
		{"bad-token-count", func(f *fixture) { f.job.MaxTokens = 513 }, ErrInput},
		{"bad-timeout", func(f *fixture) { f.job.Timeout = 301 * time.Second }, ErrInput},
		{"negative-timeout", func(f *fixture) { f.job.Timeout = -1 }, ErrInput},
		{"bad-output-limit", func(f *fixture) { f.job.StdoutBytes = DefaultStdoutBytes + 1 }, ErrInput},
		{"negative-output-limit", func(f *fixture) { f.job.StderrBytes = -1 }, ErrInput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.mutate(&f)
			called := false
			factory := func(context.Context, Command) *exec.Cmd { called = true; panic("unsafe launch") }
			_, err := runLocalInference(context.Background(), f.owner, f.job, factory)
			if err != tc.want || called {
				t.Fatalf("err=%v want=%v called=%v", err, tc.want, called)
			}
		})
	}
}

func TestLoadOwnerPolicy(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(f.dir, "owner.json")
	data := putJSON(t, path, f.owner)
	owner, err := LoadOwnerPolicy(path)
	if err != nil || !reflect.DeepEqual(owner, f.owner) {
		t.Fatalf("owner=%#v err=%v", owner, err)
	}
	for name, bad := range map[string][]byte{
		"duplicate-top":          bytes.Replace(data, []byte(`"entry_sha256":`), []byte(`"entry_sha256":"ignored","entry_sha256":`), 1),
		"duplicate-installation": bytes.Replace(data, []byte(`"python":`), []byte(`"python":"ignored","python":`), 1),
		"duplicate-runtime-hash": bytes.Replace(data, []byte(`"nexal_mlx/cli.py":`), []byte(`"nexal_mlx/cli.py":"ignored","nexal_mlx/cli.py":`), 1),
		"unknown":                append([]byte(`{"unsafe":true,`), data[1:]...),
		"trailing":               append(append([]byte{}, data...), []byte("{}")...),
	} {
		t.Run(name, func(t *testing.T) {
			put(t, path, bad, 0600)
			if _, err := LoadOwnerPolicy(path); err != ErrPolicy {
				t.Fatalf("invalid owner policy accepted: %v", err)
			}
		})
	}
}
