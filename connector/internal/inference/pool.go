package inference

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

// Pooling one model across several Macs needs `mlx_lm.server --pipeline`, which only newer mlx-lm
// releases have. It lives in its own environment so the single-Mac runtime keeps its older pins.
const (
	PoolMLXVersion          = "0.32.2"
	PoolMLXLMVersion        = "0.32.2"
	PoolTransformersVersion = "5.7.0"
)

// PoolPackages are the exact pip requirements of the pooling environment.
func PoolPackages() []string {
	return []string{"mlx==" + PoolMLXVersion, "mlx-lm==" + PoolMLXLMVersion, "transformers==" + PoolTransformersVersion}
}

// PoolVenvDir and PoolPython locate the pooling environment.
func PoolVenvDir(inferenceDir string) string { return filepath.Join(inferenceDir, "venv-pool") }
func PoolPython(inferenceDir string) string {
	return filepath.Join(PoolVenvDir(inferenceDir), "bin", "python3")
}

func poolMarker(inferenceDir string) string {
	return filepath.Join(inferenceDir, "pool-runtime.json")
}

// CheckPoolRuntime reports whether `nexal inference pool-setup` has completed.
func CheckPoolRuntime(inferenceDir string) RuntimeStatus {
	data, err := os.ReadFile(poolMarker(inferenceDir))
	if err != nil {
		return RuntimeStatus{Reason: "the pooling runtime has not been set up on this Mac"}
	}
	var m struct{ MLX, MLXLM, Transformers string }
	if json.Unmarshal(data, &m) != nil || m.MLX != PoolMLXVersion || m.MLXLM != PoolMLXLMVersion || m.Transformers != PoolTransformersVersion {
		return RuntimeStatus{Reason: "the pooling runtime was set up for other package versions; set it up again"}
	}
	py := PoolPython(inferenceDir)
	if st, err := os.Stat(py); err != nil || st.IsDir() {
		return RuntimeStatus{Reason: "the pooling runtime environment is missing; set it up again"}
	}
	return RuntimeStatus{Ready: true, Python: py}
}

func (s *RuntimeSetup) python() string {
	if s.Pool {
		return PoolPython(s.InferenceDir)
	}
	return VenvPython(s.InferenceDir)
}
func (s *RuntimeSetup) venv() string {
	if s.Pool {
		return PoolVenvDir(s.InferenceDir)
	}
	return VenvDir(s.InferenceDir)
}
func (s *RuntimeSetup) packages() []string {
	if s.Pool {
		return PoolPackages()
	}
	return RuntimePackages()
}
func (s *RuntimeSetup) status() RuntimeStatus {
	if s.Pool {
		return CheckPoolRuntime(s.InferenceDir)
	}
	return CheckRuntime(s.InferenceDir)
}
func (s *RuntimeSetup) markerPath() string {
	if s.Pool {
		return poolMarker(s.InferenceDir)
	}
	return runtimeMarker(s.InferenceDir)
}
func (s *RuntimeSetup) versions(base string) map[string]string {
	if s.Pool {
		return map[string]string{"MLX": PoolMLXVersion, "MLXLM": PoolMLXLMVersion, "Transformers": PoolTransformersVersion, "Python": base}
	}
	return map[string]string{"MLX": RuntimeMLXVersion, "MLXLM": RuntimeMLXLMVersion, "Transformers": RuntimeTransformersVersion, "Python": base}
}

// PoolServeCommand builds the command one member of a pool runs. Every member runs the same command
// with its own rank; rank 0 answers on apiHost:apiPort and the others work for it. hosts[i] is the
// "ip:port" rank i listens on for MLX's ring traffic and must be a mesh address; the hostfile is
// written to hostfilePath. Hugging Face is switched off so nothing is fetched while serving.
func PoolServeCommand(inferenceDir, id string, rank int, hosts []string, apiHost string, apiPort int, hostfilePath string) (name string, args []string, env []string, err error) {
	if !entryIDs.MatchString(id) {
		return "", nil, nil, errf("invalid_arguments", "use an id from `nexal inference status`", "invalid model id")
	}
	if len(hosts) < 2 {
		return "", nil, nil, errf("invalid_arguments", "pass every member's address, for example --hosts 100.64.0.1:8010,100.64.0.2:8010", "a pool needs at least two members")
	}
	if rank < 0 || rank >= len(hosts) {
		return "", nil, nil, errf("invalid_arguments", "use a rank from 0 to "+strconv.Itoa(len(hosts)-1), "rank %d is outside the pool", rank)
	}
	for _, h := range hosts {
		ip, port, e := net.SplitHostPort(h)
		if e != nil || net.ParseIP(ip) == nil || net.ParseIP(ip).To4() == nil {
			return "", nil, nil, errf("invalid_arguments", "use IPv4 addresses like 100.64.0.1:8010", "invalid pool member %q", h)
		}
		if p, e := strconv.Atoi(port); e != nil || p < 1024 || p > 65535 {
			return "", nil, nil, errf("invalid_arguments", "use a port between 1024 and 65535", "invalid pool member %q", h)
		}
	}
	if ip := net.ParseIP(apiHost); ip == nil || ip.IsUnspecified() {
		return "", nil, nil, errf("invalid_arguments", "pass this Mac's neXal address", "--api-host must be one specific IP address")
	}
	rt := CheckPoolRuntime(inferenceDir)
	if !rt.Ready {
		return "", nil, nil, errf("runtime_not_set_up", "run `nexal inference pool-setup` first", "%s", rt.Reason)
	}
	dir := ModelDir(inferenceDir, id)
	if st, e := os.Stat(dir); e != nil || !st.IsDir() {
		return "", nil, nil, errf("not_installed", "run `nexal inference install "+id+"` first", "%s is not installed", id)
	}
	data, _ := json.Marshal(hosts)
	if e := os.WriteFile(hostfilePath, data, 0o600); e != nil {
		return "", nil, nil, e
	}
	args = []string{"-I", "-m", "mlx_lm", "server", "--model", dir, "--pipeline", "--host", apiHost, "--port", strconv.Itoa(apiPort)}
	env = []string{"MLX_RANK=" + strconv.Itoa(rank), "MLX_HOSTFILE=" + hostfilePath, "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "HF_HUB_DISABLE_TELEMETRY=1", "PYTHONUNBUFFERED=1", "MLX_RING_VERBOSE=1", "PATH=/usr/bin:/bin"}
	return rt.Python, args, env, nil
}
