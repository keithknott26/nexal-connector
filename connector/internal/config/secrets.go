package config

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Secrets interface {
	Get(context.Context, string) (string, error)
	Put(context.Context, string, string) error
}

func RandomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func validSecret(s string) bool {
	if len(s) < 16 || len(s) > 4096 {
		return false
	}
	for _, r := range s {
		if r <= 32 || r > 126 {
			return false
		}
	}
	return true
}

func NewSecrets(path string, c Config) (Secrets, error) {
	if c.DevSecrets && c.Development {
		return FileSecrets{Dir: filepath.Join(filepath.Dir(path), "dev-secrets")}, nil
	}
	if runtime.GOOS != "darwin" {
		return nil, errors.New("production credentials require macOS Keychain; file secrets require explicit nonproduction flags")
	}
	sum := sha256.Sum256([]byte(path))
	return Keychain{Service: "com.nexal.connector." + hex.EncodeToString(sum[:8])}, nil
}

type FileSecrets struct{ Dir string }

// secretName is a closed allowlist rather than a validity check: a caller that
// can choose an arbitrary name can read or clobber a credential belonging to a
// different scope, so every stored secret is named here explicitly.
//
//   - admin: the local HTTP API bearer token
//   - host:  the coordinator host credential
//   - peer-identity: the ed25519 seed for this host's peer identity, whose
//     public half hashes to the deviceFingerprint peers pin. Stored alongside
//     the other two because it has the same lifetime and the same blast radius,
//     and because on macOS this routes it to the Keychain rather than to a file
//     the way a bespoke key store would have.
func secretName(name string) bool {
	if name == "admin" || name == "host" || name == "peer-identity" || name == "mesh-credential" {
		return true
	}
	if strings.HasPrefix(name, "enrollment-session-") {
		id := strings.TrimPrefix(name, "enrollment-session-")
		return len(id) == 36 && !strings.ContainsAny(id, " /\\\r\n\t")
	}
	return false
}
func (s FileSecrets) Get(_ context.Context, name string) (string, error) {
	if !secretName(name) {
		return "", errors.New("invalid credential scope")
	}
	b, err := ReadPrivate(filepath.Join(s.Dir, name), 4096)
	if err != nil {
		return "", errors.New("credential unavailable")
	}
	if !validSecret(string(b)) {
		return "", errors.New("invalid credential")
	}
	return string(b), nil
}
func (s FileSecrets) Put(_ context.Context, name, token string) error {
	if !secretName(name) || !validSecret(token) {
		return errors.New("invalid credential")
	}
	return AtomicPrivate(filepath.Join(s.Dir, name), []byte(token))
}

// Keychain uses security's stdin interpreter for writes, never a password in
// argv or environment. Both output streams are suppressed to avoid interpreter
// echo, prompt or diagnostic leakage. There is no shell and no -A blanket ACL.
type Keychain struct{ Service string }

func (k Keychain) Get(ctx context.Context, name string) (string, error) {
	if !secretName(name) {
		return "", errors.New("invalid credential scope")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", k.Service, "-a", name, "-w")
	b, err := cmd.Output()
	if err != nil {
		return "", errors.New("Keychain credential unavailable or access denied")
	}
	token := strings.TrimSuffix(string(b), "\n")
	if !validSecret(token) {
		return "", errors.New("invalid Keychain credential")
	}
	return token, nil
}
func keychainQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
func (k Keychain) Put(ctx context.Context, name, token string) error {
	if !secretName(name) || !validSecret(token) {
		return errors.New("invalid credential")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader("add-generic-password -U -s " + keychainQuote(k.Service) + " -a " + keychainQuote(name) + " -w " + keychainQuote(token) + "\n")
	if err := cmd.Run(); err != nil {
		return errors.New("Keychain write failed or access denied")
	}
	// The interactive command's exit status alone may not reflect subcommand
	// failure; read back and compare without printing either value.
	got, err := k.Get(ctx, name)
	if err != nil || got != token {
		return errors.New("Keychain write verification failed")
	}
	return nil
}
