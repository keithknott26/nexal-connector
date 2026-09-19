// Package config stores nonsecret policy separately from credentials.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const Version = "0.1.0"

type Tunnel struct {
	Binary             string `json:"binary"`
	SHA256             string `json:"sha256"`
	Version            string `json:"version"`
	Architecture       string `json:"architecture"`
	SourceURL          string `json:"sourceUrl"`
	VerificationMethod string `json:"verificationMethod"`
	TokenFile          string `json:"tokenFile"`
	Hostname           string `json:"hostname"`
}

type Config struct {
	Version            int     `json:"version"`
	Coordinator        string  `json:"coordinator"`
	Name               string  `json:"name"`
	HostID             string  `json:"hostId,omitempty"`
	Listen             string  `json:"listen"`
	Development        bool    `json:"development"`
	DevSecrets         bool    `json:"devSecrets"`
	Paused             bool    `json:"paused"`
	MarketplaceEnabled bool    `json:"marketplaceEnabled"`
	MemoryLimitBytes   uint64  `json:"memoryLimitBytes"`
	ReserveMemoryBytes uint64  `json:"reserveMemoryBytes"`
	IdleSeconds        uint64  `json:"idleSeconds"`
	Tunnel             *Tunnel `json:"tunnel,omitempty"`
}

func DefaultPath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(h, "Library", "Application Support", "Nexal", "config.json"), nil
	}
	return filepath.Join(h, ".config", "nexal", "config.json"), nil
}

// ValidateURL never allows credentials, URL ambiguities, query-based secrets, or
// plaintext except a numeric loopback IP in explicit development mode.
func ValidateURL(raw string, dev bool) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return errors.New("invalid coordinator URL")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		u.Opaque != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") ||
		strings.ContainsAny(raw, "\\\r\n\t ") {
		return errors.New("coordinator must be an origin URL without credentials, path, query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && dev && ip != nil && ip.IsLoopback() {
		return nil
	}
	if u.Scheme != "https" {
		return errors.New("HTTPS required; HTTP is allowed only for explicit development numeric loopback")
	}
	if u.Hostname() == "" {
		return errors.New("missing coordinator hostname")
	}
	return nil
}

func ValidateListen(s string) error {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return errors.New("listen must be numeric loopback host:port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("local API must bind only to numeric loopback")
	}
	p, err := net.LookupPort("tcp", port)
	if err != nil || p < 1 || p > 65535 {
		return errors.New("invalid listen port")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return errors.New("listen port must be numeric")
		}
	}
	return nil
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return errors.New("unsupported config version")
	}
	if err := ValidateURL(c.Coordinator, c.Development); err != nil {
		return err
	}
	if err := ValidateListen(c.Listen); err != nil {
		return err
	}
	if c.DevSecrets && !c.Development {
		return errors.New("file secrets are forbidden in production")
	}
	if c.MarketplaceEnabled {
		return errors.New("public execution is gated pending verified tunnel dispatch")
	}
	if len(strings.TrimSpace(c.Name)) < 1 || len(c.Name) > 80 {
		return errors.New("host name must contain 1–80 bytes and not be blank")
	}
	if c.MemoryLimitBytes < 64<<20 || c.MemoryLimitBytes > 8<<30 {
		return errors.New("approved memory limit must be 64 MiB–8 GiB")
	}
	if c.ReserveMemoryBytes < 128<<20 || c.ReserveMemoryBytes > 1<<40 {
		return errors.New("owner memory reserve must be 128 MiB–1 TiB")
	}
	if c.IdleSeconds < 30 || c.IdleSeconds > 86400 {
		return errors.New("idle threshold must be 30–86400 seconds")
	}
	return nil
}

func Load(path string) (Config, error) {
	var c Config
	b, err := ReadPrivate(path, 64<<10)
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, errors.New("invalid configuration JSON")
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return c, errors.New("trailing configuration data")
	}
	return c, c.Validate()
}

func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return AtomicPrivate(path, append(b, '\n'))
}

// AtomicPrivate writes to a private temporary file, fsyncs, and atomically
// renames it. Existing files or leaf directories with loose modes are rejected.
func AtomicPrivate(path string, data []byte) error {
	if !filepath.IsAbs(path) {
		return errors.New("private path must be absolute")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("cannot create private directory")
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return errors.New("private directory must have mode 0700 and not be a symlink")
	}
	if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0) {
		return errors.New("private file must be regular and mode 0600")
	} else if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot inspect private file")
	}
	f, err := os.CreateTemp(dir, ".nexal-*")
	if err != nil {
		return errors.New("cannot create private temporary file")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot write private file")
	}
	if err = os.Rename(tmp, path); err != nil {
		return errors.New("cannot atomically replace private file")
	}
	d, err := os.Open(dir)
	if err == nil {
		defer d.Close()
		_ = d.Sync()
	}
	return nil
}

func ReadPrivate(path string, limit int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("private file unavailable: %w", err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > limit {
		return nil, errors.New("private file must be regular, bounded and mode 0600")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read private file")
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return nil, errors.New("private file changed during open")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("private file exceeds limit")
	}
	return b, nil
}
