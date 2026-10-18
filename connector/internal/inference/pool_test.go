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
	if name != PoolPython(d) || !strings.Contains(strings.Join(args, " "), "--pipeline") {
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
