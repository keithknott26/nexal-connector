package inference

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// One-click runtime for "simple mode": a private Python environment inside the neXal config folder
// with exactly the pinned MLX packages, and a local OpenAI-compatible chat server over an installed,
// hash-verified model. Nothing here downloads a model or runs model-supplied code.

// Pinned runtime packages (the same versions runtimes/requirements.in and catalog.json name).
const (
	RuntimeMLXVersion          = "0.29.4"
	RuntimeMLXLMVersion        = "0.28.4"
	RuntimeTransformersVersion = "4.57.6"

	// DefaultServePort is where the local chat server listens.
	DefaultServePort = 8080
)

// RuntimePackages are the exact pip requirements installed into the environment.
func RuntimePackages() []string {
	return []string{
		"mlx==" + RuntimeMLXVersion,
		"mlx-lm==" + RuntimeMLXLMVersion,
		"transformers==" + RuntimeTransformersVersion,
	}
}

// VenvDir is the private environment folder.
func VenvDir(inferenceDir string) string { return filepath.Join(inferenceDir, "venv") }

// VenvPython is the environment's interpreter.
func VenvPython(inferenceDir string) string {
	return filepath.Join(VenvDir(inferenceDir), "bin", "python3")
}

// ModelDir is where an installed model's files live.
func ModelDir(inferenceDir, id string) string { return modelDirPath(inferenceDir, id) }

func runtimeMarker(inferenceDir string) string { return filepath.Join(inferenceDir, "runtime.json") }

// SetupEvent is one line of `nexal inference setup --json`.
type SetupEvent struct {
	Event   string `json:"event"` // step | done
	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`
}

// RuntimeStatus says whether the environment is ready, for the app's picker.
type RuntimeStatus struct {
	Ready  bool   `json:"ready"`
	Python string `json:"python,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// CheckRuntime reports whether setup has completed (marker present with the pinned versions and an
// interpreter on disk). It does not start Python.
func CheckRuntime(inferenceDir string) RuntimeStatus {
	data, err := os.ReadFile(runtimeMarker(inferenceDir))
	if err != nil {
		return RuntimeStatus{Reason: "the AI runtime has not been set up on this Mac"}
	}
	var m struct {
		MLX, MLXLM, Transformers string
	}
	if json.Unmarshal(data, &m) != nil || m.MLX != RuntimeMLXVersion || m.MLXLM != RuntimeMLXLMVersion || m.Transformers != RuntimeTransformersVersion {
		return RuntimeStatus{Reason: "the AI runtime was set up for other package versions; set it up again"}
	}
	py := VenvPython(inferenceDir)
	if st, err := os.Stat(py); err != nil || st.IsDir() {
		return RuntimeStatus{Reason: "the AI runtime environment is missing; set it up again"}
	}
	return RuntimeStatus{Ready: true, Python: py}
}

// Commander runs one external command and returns its combined output.
type Commander func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecCommander is the real Commander.
func ExecCommander(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// RuntimeSetup creates the environment.
type RuntimeSetup struct {
	InferenceDir string
	GOOS, GOARCH string
	Run          Commander
	Emit         func(ev any)
	// PythonCandidates are tried in order (default: Homebrew, then the system Python).
	PythonCandidates []string
}

func (s *RuntimeSetup) emit(ev any) {
	if s.Emit != nil {
		s.Emit(ev)
	}
}

func (s *RuntimeSetup) step(step, msg string) {
	s.emit(SetupEvent{Event: "step", Step: step, Message: msg})
}

func tail(b []byte) string {
	t := strings.TrimSpace(string(b))
	if len(t) > 600 {
		t = "…" + t[len(t)-600:]
	}
	return t
}

// Setup is idempotent: with a ready environment it only confirms the imports.
func (s *RuntimeSetup) Setup(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			e := AsError(err)
			err = e
			s.emit(ErrorEvent{Event: "error", Code: e.Code, Message: e.Message, Fix: e.Fix})
		}
	}()
	if s.GOOS != "darwin" || s.GOARCH != "arm64" {
		return errf("unsupported_platform", "run this on a Mac with Apple silicon", "MLX needs a Mac with Apple silicon (this is %s/%s)", s.GOOS, s.GOARCH)
	}
	run := s.Run
	if run == nil {
		run = ExecCommander
	}
	if err := os.MkdirAll(s.InferenceDir, 0o700); err != nil {
		return err
	}
	py := VenvPython(s.InferenceDir)
	check := func() error {
		out, err := run(ctx, py, "-c", "import mlx.core, mlx_lm; print(mlx_lm.__version__)")
		if err != nil {
			return fmt.Errorf("%s", tail(out))
		}
		return nil
	}
	if CheckRuntime(s.InferenceDir).Ready && check() == nil {
		s.step("ready", "The AI runtime is already set up")
		s.emit(SetupEvent{Event: "done"})
		return nil
	}

	s.step("python", "Looking for Python 3")
	base := ""
	cands := s.PythonCandidates
	if len(cands) == 0 {
		cands = []string{"/opt/homebrew/bin/python3", "/usr/local/bin/python3", "/usr/bin/python3"}
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err != nil {
			continue
		}
		if c == "/usr/bin/python3" {
			// Without the Xcode command line tools this stub opens an install dialog instead of running.
			if _, err := run(ctx, "/usr/bin/xcode-select", "-p"); err != nil {
				continue
			}
		}
		out, err := run(ctx, c, "-c", "import sys; raise SystemExit(0 if sys.version_info >= (3, 10) else 1)")
		_ = out
		if err == nil {
			base = c
			break
		}
	}
	if base == "" {
		return errf("python_missing", "install Python 3.10 or newer (for example `brew install python`), then try again", "no usable Python 3.10 or newer was found")
	}

	s.step("venv", "Creating the private environment")
	_ = os.RemoveAll(VenvDir(s.InferenceDir))
	if out, err := run(ctx, base, "-m", "venv", VenvDir(s.InferenceDir)); err != nil {
		return errf("venv_failed", "check free disk space and try again", "could not create the environment: %s", tail(out))
	}

	s.step("packages", "Downloading MLX (about 200 MB)")
	args := append([]string{"-m", "pip", "install", "--disable-pip-version-check", "--no-input", "--only-binary=:all:"}, RuntimePackages()...)
	if out, err := run(ctx, py, args...); err != nil {
		return errf("pip_failed", "check the internet connection and try again", "installing the MLX packages failed: %s", tail(out))
	}

	s.step("verify", "Checking MLX loads")
	if err := check(); err != nil {
		return errf("runtime_broken", "set it up again; if this repeats, send a diagnostics report", "MLX was installed but does not load: %v", err)
	}
	marker, _ := json.Marshal(map[string]string{"MLX": RuntimeMLXVersion, "MLXLM": RuntimeMLXLMVersion, "Transformers": RuntimeTransformersVersion, "Python": base})
	if err := os.WriteFile(runtimeMarker(s.InferenceDir), marker, 0o600); err != nil {
		return err
	}
	s.emit(SetupEvent{Event: "done"})
	return nil
}

// ServeCommand builds the argument list that serves one installed model through mlx_lm's
// OpenAI-compatible server. host must be an IP address (the app passes the mesh address, or
// 127.0.0.1); the server has no sign-in of its own, so it is never bound to all interfaces.
// Hugging Face is switched off so nothing can be fetched while serving.
func ServeCommand(inferenceDir, id, host string, port int) (name string, args []string, env []string, err error) {
	if !entryIDs.MatchString(id) {
		return "", nil, nil, errf("invalid_arguments", "use an id from `nexal inference status`", "invalid model id")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		return "", nil, nil, errf("invalid_arguments", "pass this Mac's neXal address or 127.0.0.1", "--host must be one specific IP address, not %q", host)
	}
	if port < 1024 || port > 65535 {
		return "", nil, nil, errf("invalid_arguments", "use a port between 1024 and 65535", "invalid port %d", port)
	}
	rt := CheckRuntime(inferenceDir)
	if !rt.Ready {
		return "", nil, nil, errf("runtime_not_set_up", "run `nexal inference setup` first", "%s", rt.Reason)
	}
	dir := ModelDir(inferenceDir, id)
	if st, e := os.Stat(dir); e != nil || !st.IsDir() {
		return "", nil, nil, errf("not_installed", "run `nexal inference install "+id+"` first", "%s is not installed", id)
	}
	args = []string{"-I", "-m", "mlx_lm.server", "--model", dir, "--host", ip.String(), "--port", strconv.Itoa(port)}
	env = []string{"HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "HF_HUB_DISABLE_TELEMETRY=1", "PATH=/usr/bin:/bin"}
	return rt.Python, args, env, nil
}
