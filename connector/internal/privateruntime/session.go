// Package privateruntime is a public bootstrap/verifier, not a proprietary model
// implementation. It never embeds runtime bytes, provider secrets or signing keys.
package privateruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var ErrClosed = errors.New("private runtime authorization unavailable")
var safePath = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_./-]{0,200}$`)
var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var sha = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Envelope struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
	PublicKey string `json:"publicKey"`
}
type Lease struct {
	SchemaVersion int       `json:"schemaVersion"`
	ServerNow     time.Time `json:"serverNow"`
	ExpiresAt     time.Time `json:"expiresAt"`
}
type Session struct {
	Lease
	SessionID string   `json:"sessionId"`
	NetworkID string   `json:"networkId"`
	Bundle    Envelope `json:"bundle"`
}
type File struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	ObjectKey string `json:"objectKey"`
}
type Manifest struct {
	SchemaVersion int    `json:"schemaVersion"`
	ReleaseID     string `json:"releaseId"`
	Entrypoint    string `json:"entrypoint"`
	Files         []File `json:"files"`
}
type API interface {
	OpenRuntime(context.Context, string) (Session, error)
	RenewRuntime(context.Context, string, string) (Lease, error)
	DownloadRuntime(context.Context, string, string, int, io.Writer, int64) error
	CloseRuntime(context.Context, string, string) error
}

func Verify(e Envelope) (Manifest, error) {
	var out Manifest
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil || len(raw) > 40*1024 {
		return out, ErrClosed
	}
	public, err := base64.StdEncoding.DecodeString(e.PublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return out, ErrClosed
	}
	signature, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil || !ed25519.Verify(public, raw, signature) {
		return out, ErrClosed
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil || decoder.Decode(new(any)) != io.EOF || out.SchemaVersion != 1 || !safeID.MatchString(out.ReleaseID) || out.Entrypoint != "runtime" || len(out.Files) < 1 || len(out.Files) > 128 {
		return out, ErrClosed
	}
	seen := map[string]bool{}
	var total int64
	for _, f := range out.Files {
		if !safePath.MatchString(f.Path) || filepath.Clean(f.Path) != f.Path || filepath.IsAbs(f.Path) || f.Path == "session.json" || f.Path == "session.tmp" || seen[f.Path] || !sha.MatchString(f.SHA256) || f.Size < 1 || f.Size > 16<<30 {
			return out, ErrClosed
		}
		// A signed manifest must not make any file an ancestor of another file.
		for parent := filepath.Dir(f.Path); parent != "."; parent = filepath.Dir(parent) {
			if seen[parent] {
				return out, ErrClosed
			}
		}
		seen[f.Path] = true
		total += f.Size
	}
	for _, f := range out.Files {
		for parent := filepath.Dir(f.Path); parent != "."; parent = filepath.Dir(parent) {
			if seen[parent] {
				return out, ErrClosed
			}
		}
	}
	if !seen["runtime"] || total > 32<<30 {
		return out, ErrClosed
	}
	return out, nil
}

func leaseDuration(l Lease) (time.Duration, error) {
	duration := l.ExpiresAt.Sub(l.ServerNow)
	if l.SchemaVersion != 1 || duration <= 0 || duration > 30*time.Second {
		return 0, ErrClosed
	}
	return duration, nil
}

// Manager owns session-scoped directories. Every execution downloads fresh bytes;
// no cached payload survives a normal stop, disconnect, revocation or failed launch.
type Manager struct {
	API       API
	HostID    string
	Root      string
	Connected func() bool
	// Run may be replaced by a test harness. Production uses the fixed executable.
	Run  func(context.Context, string, string) error
	Poll time.Duration
}

func (m Manager) RunLoop(ctx context.Context) error {
	if m.API == nil || !safeID.MatchString(m.HostID) || m.Connected == nil || !filepath.IsAbs(m.Root) {
		return ErrClosed
	}
	if err := os.MkdirAll(m.Root, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(m.Root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return ErrClosed
	}
	unlock, err := lock(m.Root)
	if err != nil {
		return err
	}
	defer unlock()
	// The lock excludes another bootstrap instance; remove abandoned private data.
	entries, err := os.ReadDir(m.Root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if len(entry.Name()) > 8 && entry.Name()[:8] == "session-" {
			if err := os.RemoveAll(filepath.Join(m.Root, entry.Name())); err != nil {
				return err
			}
		}
	}
	if m.Poll <= 0 {
		m.Poll = 2 * time.Second
	}
	if m.Run == nil {
		m.Run = runProcess
	}
	for ctx.Err() == nil {
		if m.Connected() {
			_ = m.once(ctx)
		}
		delay := m.Poll
		// No tight download/exec loop when the coordinator is unconfigured or a
		// runtime fails: each retry still begins a fresh authenticated session.
		if m.Connected() {
			delay = 30 * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func (m Manager) once(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	started := time.Now()
	session, err := m.API.OpenRuntime(ctx, m.HostID)
	if err != nil {
		return err
	}
	defer func() {
		finish, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = m.API.CloseRuntime(finish, m.HostID, session.SessionID)
	}()
	if !m.Connected() || !safeID.MatchString(session.SessionID) || !safeID.MatchString(session.NetworkID) {
		return ErrClosed
	}
	ttl, err := leaseDuration(session.Lease)
	if err != nil {
		return err
	}
	manifest, err := Verify(session.Bundle)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp(m.Root, "session-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "session.json")
	var mu sync.Mutex
	expires := started.Add(ttl)
	writeLease := func() error {
		raw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "sessionId": session.SessionID, "networkId": session.NetworkID, "hostId": m.HostID, "expiresAt": expires.UnixMilli()})
		tmp := filepath.Join(directory, "session.tmp")
		if err := os.WriteFile(tmp, raw, 0600); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	}
	if !time.Now().Before(expires) || writeLease() != nil {
		return ErrClosed
	}
	monitorDone := make(chan struct{})
	renewDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(m.Poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				valid := time.Now().Before(expires)
				mu.Unlock()
				if !valid || !m.Connected() {
					cancel()
					return
				}
			}
		}
	}()
	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				began := time.Now()
				lease, err := m.API.RenewRuntime(ctx, m.HostID, session.SessionID)
				if err != nil {
					cancel()
					return
				}
				duration, err := leaseDuration(lease)
				if err != nil {
					cancel()
					return
				}
				mu.Lock()
				expires = began.Add(duration)
				err = writeLease()
				mu.Unlock()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-monitorDone; <-renewDone }()
	for index, file := range manifest.Files {
		if ctx.Err() != nil || !m.Connected() {
			return ErrClosed
		}
		dest := filepath.Join(directory, file.Path)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		writer, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		sum := sha256.New()
		download, stop := context.WithTimeout(ctx, 10*time.Minute)
		err = m.API.DownloadRuntime(download, m.HostID, session.SessionID, index, io.MultiWriter(writer, sum), file.Size)
		stop()
		info, statErr := writer.Stat()
		closeErr := writer.Close()
		if err != nil || statErr != nil || closeErr != nil || info.Size() != file.Size || hex.EncodeToString(sum.Sum(nil)) != file.SHA256 {
			return ErrClosed
		}
		if err := os.Chmod(dest, 0500); err != nil {
			return err
		} // Executability is signed by inclusion in the runtime bundle.
	}
	if ctx.Err() != nil || !m.Connected() {
		return ErrClosed
	}
	return m.Run(ctx, directory, path)
}
