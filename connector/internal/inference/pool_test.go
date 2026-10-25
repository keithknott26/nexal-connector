package inference

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func poolDir(t *testing.T) string {
	d := t.TempDir()
	os.MkdirAll(PoolVenvDir(d)+"/bin", 0o700)
	os.WriteFile(PoolPython(d), nil, 0o700)
	os.WriteFile(poolMarker(d), []byte(`{"MLX":"`+PoolMLXVersion+`","MLXLM":"`+PoolMLXLMVersion+`","Transformers":"`+PoolTransformersVersion+`"}`), 0o600)
	os.MkdirAll(ModelDir(d, "m1"), 0o700)
	return d
}

func TestPoolServeCommand(t *testing.T) {
	d := poolDir(t)
	hf := filepath.Join(d, "h.json")
	hosts := []string{"100.64.0.1:8010", "100.64.0.2:8010"}
	name, args, env, err := PoolServeCommand(d, "m1", 1, hosts, "100.64.0.2", 8001, hf)
	if err != nil {
		t.Fatal(err)
	}
	if name != PoolPython(d) || strings.Contains(strings.Join(args, " "), "--pipeline") {
		t.Fatalf("%s %v", name, args)
	}
	if !strings.Contains(strings.Join(env, " "), "MLX_RANK=1") {
		t.Fatal(env)
	}
	b, _ := os.ReadFile(hf)
	if string(b) != `["100.64.0.1:8010","100.64.0.2:8010"]` {
		t.Fatal(string(b))
	}
}

func TestPoolServeCommandRejects(t *testing.T) {
	d := poolDir(t)
	hf := filepath.Join(d, "h.json")
	ok := []string{"100.64.0.1:8010", "100.64.0.2:8010"}
	for name, c := range map[string]func() error{
		"one member": func() error { _, _, _, e := PoolServeCommand(d, "m1", 0, ok[:1], "100.64.0.1", 8001, hf); return e },
		"bad rank":   func() error { _, _, _, e := PoolServeCommand(d, "m1", 2, ok, "100.64.0.1", 8001, hf); return e },
		"ipv6 host": func() error {
			_, _, _, e := PoolServeCommand(d, "m1", 0, []string{"[::1]:8010", ok[1]}, "100.64.0.1", 8001, hf)
			return e
		},
		"all ifaces": func() error { _, _, _, e := PoolServeCommand(d, "m1", 0, ok, "0.0.0.0", 8001, hf); return e },
		"no model":   func() error { _, _, _, e := PoolServeCommand(d, "zz", 0, ok, "100.64.0.1", 8001, hf); return e },
		"no runtime": func() error {
			_, _, _, e := PoolServeCommand(t.TempDir(), "m1", 0, ok, "100.64.0.1", 8001, hf)
			return e
		},
	} {
		if c() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestOrphanPoolServers(t *testing.T) {
	d := "/Users/k/Library/Application Support/Nexal/inference"
	m := modelsRoot(d) + "/qwen3-4b-q4"
	ps := strings.Join([]string{
		"  101     1 /opt/py/Python -I -m mlx_lm.server --model " + m + " --pipeline --host 100.86.1.1 --port 8004", // old form, orphaned
		"  102     1 /opt/py/Python -I -m mlx_lm server --model " + m + " --pipeline --host 100.86.1.1 --port 8004", // new form, orphaned
		"  103  5000 /opt/py/Python -I -m mlx_lm server --model " + m + " --pipeline --host 100.86.1.1 --port 8004", // its helper is alive
		"  104     1 /opt/py/Python -I -m mlx_lm server --model /elsewhere/models/x --pipeline --host 1.1.1.1",      // another directory
		"  105     1 /opt/py/Python -I -m mlx_lm.server --model " + m + " --host 100.86.1.1 --port 8001",            // a single-Mac server, not a pool one
		"  108     1 /opt/py/Python -I -m mlx_lm server --model " + m + " --host 100.86.1.1 --port 8004",            // tensor-parallel pool server, orphaned
		"  106     1 /usr/bin/vim notes " + m + " --pipeline",                                                       // not mlx_lm
		"  107     1 /opt/py/Python -I -m mlx_lm server --model " + m + " --pipeline --host 100.86.1.1",             // this process itself
		"garbage",
	}, "\n")
	got := orphanPoolServers(ps, d, 107)
	if len(got) != 3 || got[0] != 101 || got[1] != 102 || got[2] != 108 {
		t.Fatalf("picked %v, want [101 102 108]", got)
	}
	if len(orphanPoolServers("", d, 1)) != 0 {
		t.Fatal("empty input")
	}
}

func TestOrphanServeServers(t *testing.T) {
	d := "/Users/k/Library/Application Support/Nexal/inference"
	m := modelsRoot(d) + "/qwen3-4b-q4"
	shim := "/opt/py/Python -I -c import sys, json, time, os, threading\nfrom mlx_lm import server as s\nparent = os.getppid()\n s.main()\n --model " + m + " --host 127.0.0.1 --port "
	ps := strings.Join([]string{
		"  201     1 " + shim + "51000",                                               // orphaned
		"  202  4000 " + shim + "51001",                                               // its helper is alive
		"  203     1 " + strings.Replace(shim, m, "/elsewhere/models/x", 1) + "1",     // another directory
		"  204     1 /opt/py/Python -I -m mlx_lm server --model " + m + " --pipeline", // a pool server: not this reaper's
		"  205     1 " + shim + "51002",                                               // this process itself
	}, "\n")
	got := orphanServeServers(ps, d, 205)
	if len(got) != 1 || got[0] != 201 {
		t.Fatalf("picked %v, want [201]", got)
	}
}

func TestPoolUsesPipeline(t *testing.T) {
	d := poolDir(t)
	cfg := func(modelType string) {
		os.WriteFile(filepath.Join(ModelDir(d, "m1"), "config.json"), []byte(`{"model_type":"`+modelType+`"}`), 0o600)
	}
	hf := filepath.Join(d, "h.json")
	run := func() string {
		_, args, _, err := PoolServeCommand(d, "m1", 0, []string{"100.64.0.1:8010", "100.64.0.2:8010"}, "100.64.0.1", 8001, hf)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(args, " ")
	}
	cfg("qwen3") // dense Qwen3 has no pipeline stages in mlx-lm, only tensor parallelism
	if got := run(); strings.Contains(got, "--pipeline") || !strings.Contains(got, "-m mlx_lm server --model") {
		t.Fatal(got)
	}
	cfg("qwen3_moe")
	if got := run(); !strings.Contains(got, "--pipeline") {
		t.Fatal(got)
	}
	os.Remove(filepath.Join(ModelDir(d, "m1"), "config.json")) // unreadable: tensor parallelism
	if got := run(); strings.Contains(got, "--pipeline") {
		t.Fatal(got)
	}
}

// Every model server keeps MLX off MPI. The solo server has no ring environment, so without this MLX falls
// through to MPI_Init, which aborts on a Mac with Open MPI installed.
func TestServersDisableMPI(t *testing.T) {
	d := poolDir(t)
	hf := filepath.Join(d, "h.json")
	hosts := []string{"100.64.0.1:8010", "100.64.0.2:8010"}
	_, _, ring, err := PoolServeCommand(d, "m1", 1, hosts, "100.64.0.2", 8001, hf)
	if err != nil {
		t.Fatal(err)
	}
	_, _, solo, err := SoloServeCommand(d, "m1", "100.64.0.1", 8001)
	if err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string][]string{"ring member": ring, "solo": solo} {
		found := false
		for _, e := range env {
			if e == disableMPI {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s server environment lacks %s: %v", name, disableMPI, env)
		}
	}
	for _, e := range solo {
		if strings.HasPrefix(e, "MLX_RANK=") || strings.HasPrefix(e, "MLX_HOSTFILE=") {
			t.Fatalf("the solo server must not join a ring: %v", solo)
		}
	}
}
