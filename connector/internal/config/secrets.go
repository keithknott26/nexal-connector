package config

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Secrets interface {
	Get(context.Context, string) (string, error)
	Put(context.Context, string, string) error
	Delete(context.Context, string) error
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
	if runtime.GOOS == "linux" {
		// A Linux server (neXal storage) has no Keychain. Its service manager
		// provides a private directory (systemd StateDirectory, 0700, owned by the
		// service user); nothing else is accepted.
		if dir := os.Getenv(ServerSecretsDirEnv); dir != "" {
			if err := checkServerSecretsDir(dir); err != nil {
				return nil, err
			}
			return FileSecrets{Dir: dir}, nil
		}
	}
	if runtime.GOOS != "darwin" {
		return nil, errors.New("production credentials require macOS Keychain (or, on a Linux server, " + ServerSecretsDirEnv + "); file secrets require explicit nonproduction flags")
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
func (s FileSecrets) Delete(_ context.Context, name string) error {
	if !secretName(name) {
		return errors.New("invalid credential scope")
	}
	err := os.Remove(filepath.Join(s.Dir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("credential removal failed")
	}
	return nil
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
func (k Keychain) Delete(ctx context.Context, name string) error {
	if !secretName(name) {
		return errors.New("invalid credential scope")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "delete-generic-password", "-s", k.Service, "-a", name)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 { return nil }
		return errors.New("Keychain credential removal failed or access denied")
	}
	return nil
}

// ServerSecretsDirEnv names the private credential directory of a Linux server
// connector (see connector/deploy/nexal-storage/README.md).
const ServerSecretsDirEnv = "NEXAL_SECRETS_DIR"

// checkServerSecretsDir accepts only an absolute, real (not symlinked) directory
// owned by this process's user with no group or other permissions.
func checkServerSecretsDir(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New(ServerSecretsDirEnv + " must be an absolute path")
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return errors.New(ServerSecretsDirEnv + " must be an existing directory")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return errors.New(ServerSecretsDirEnv + " must not be accessible to group or others (chmod 700)")
	}
	if uid, ok := ownerUID(fi); ok && uid != os.Getuid() {
		return errors.New(ServerSecretsDirEnv + " must be owned by the connector's user")
	}
	return nil
}
