package cybersecurity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type scanOutput struct {
	mu       sync.Mutex
	b        bytes.Buffer
	overflow bool
}

func (b *scanOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.b.Len()+n > 64<<10 {
		b.overflow = true
		return 0, errors.New("engine output limit")
	}
	return b.b.Write(p)
}
func engineCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = time.Second
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=/var/empty"}
	var out, stderr scanOutput
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil || out.overflow || stderr.overflow {
		return nil, errors.New("engine_failed")
	}
	return out.b.Bytes(), nil
}
func checkEngine(ctx context.Context, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("engine_unavailable")
	}
	stat, err := os.Lstat(path)
	if err != nil || !stat.Mode().IsRegular() || stat.Mode()&0111 == 0 || stat.Mode().Perm()&0022 != 0 {
		return "", errors.New("engine_unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("engine_unavailable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 128<<20))
	if err != nil || len(b) >= 128<<20 {
		return "", errors.New("engine_unavailable")
	}
	out, err := engineCommand(ctx, path, "--version")
	if err != nil || strings.TrimSpace(string(out)) != "yara-x-cli "+EngineVersion {
		return "", errors.New("engine_unavailable")
	}
	return digest(b), nil
}
func scanBytes(ctx context.Context, engine, compiled, target string) ([]string, error) {
	out, err := engineCommand(ctx, engine, "scan", "--compiled-rules", "--output-format=ndjson", "--no-mmap", "--threads=1", "--timeout=5", "--disable-console-logs", compiled, target)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	var result struct {
		Rules []struct {
			Identifier string `json:"identifier"`
		} `json:"rules"`
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	if dec.Decode(&result) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, errors.New("engine_failed")
	}
	ids := []string{}
	for _, rule := range result.Rules {
		if !contains(rule.Identifier, "nexal_eicar_test", "nexal_script_download_execute", "nexal_powershell_encoded_hidden", "nexal_python_reverse_shell", "nexal_macho_persistence_credentials") {
			return nil, errors.New("engine_failed")
		}
		ids = append(ids, rule.Identifier)
	}
	return ids, nil
}
