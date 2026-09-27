package cybersecurity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Canary watches only its own decoy. It never inspects user documents, opens a
// network listener, claims to detect reads, or executes a mitigation.
type Canary struct{ Directory string }
type CanaryState struct {
	Enabled       bool   `json:"enabled"`
	Baseline      string `json:"baseline"`
	Observed      string `json:"observed"`
	LastCheckedAt string `json:"lastCheckedAt,omitempty"`
	Status        string `json:"status"`
	Pending       *Event `json:"pending,omitempty"`
}

const canaryName = "nexal-decoy.txt"

func (c Canary) locked(fn func(*os.Root, *CanaryState) error) error {
	// The parent is the existing private connector state directory, never an
	// arbitrary user path. OpenRoot constrains all subsequent path resolution.
	parent, err := os.OpenRoot(c.Directory)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := parent.Mkdir("security-canary", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	stat, err := parent.Lstat("security-canary")
	if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 || stat.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe canary directory")
	}
	root, err := parent.OpenRoot("security-canary")
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := root.OpenFile("lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if stat, err := lock.Stat(); err != nil || !stat.Mode().IsRegular() {
		return errors.New("unsafe canary lock")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("canary state busy")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state := CanaryState{Status: "disabled"}
	f, err := root.OpenFile("state.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == nil {
		defer f.Close()
		if stat, err := f.Stat(); err != nil || !stat.Mode().IsRegular() {
			return errors.New("unsafe canary state")
		}
		b, e := io.ReadAll(io.LimitReader(f, 32769))
		if e != nil || len(b) > 32768 || json.Unmarshal(b, &state) != nil {
			return errors.New("invalid canary state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fn(root, &state)
}
func saveCanary(root *os.Root, state *CanaryState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	// Unique create avoids overwriting a planted temporary-file symlink.
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	name := "state-" + hex.EncodeToString(id) + ".tmp"
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := root.Rename(name, "state.json"); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func canaryFingerprint(root *os.Root) string {
	f, err := root.OpenFile(canaryName, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if err != nil {
		return "unsafe"
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 4096 {
		return "unsafe"
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 {
		return "unsafe"
	}
	hash := sha256.Sum256(b)
	return hex.EncodeToString(hash[:])
}

// Configure preserves changed/missing evidence. Re-enabling an existing decoy
// does not silently reset its baseline or remove a pending report.
func (c Canary) Configure(enabled bool) error {
	return c.locked(func(root *os.Root, s *CanaryState) error {
		if enabled && s.Baseline == "" {
			random := make([]byte, 32)
			if _, err := rand.Read(random); err != nil {
				return err
			}
			f, err := root.OpenFile(canaryName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = f.WriteString("neXal integrity canary — not a real credential\n" + hex.EncodeToString(random) + "\n")
			if err == nil {
				err = f.Sync()
			}
			f.Close()
			if err != nil {
				return err
			}
			s.Baseline = canaryFingerprint(root)
			if len(s.Baseline) != 64 {
				return errors.New("canary creation changed unexpectedly")
			}
			s.Observed = s.Baseline
		}
		s.Enabled = enabled
		s.Status = "disabled"
		if enabled {
			s.Status = "watching"
			if s.Observed != s.Baseline {
				s.Status = "alert"
			}
		}
		return saveCanary(root, s)
	})
}
func (c Canary) Status() (CanaryState, error) {
	var result CanaryState
	err := c.locked(func(_ *os.Root, s *CanaryState) error { result = *s; result.Pending = nil; return nil })
	return result, err
}

// Tick persists the alert before network delivery. Failed delivery retains the
// exact event ID and content for an idempotent retry after restart.
func (c Canary) Tick(ctx context.Context, now time.Time, report func(context.Context, Event) error) error {
	return c.locked(func(root *os.Root, s *CanaryState) error {
		if !s.Enabled {
			return nil
		}
		// After the seven-day server retention window, observe the decoy
		// anew rather than wedging forever on an expired offline event.
		if s.Pending != nil {
			at, err := time.Parse(TimeLayout, s.Pending.ObservedAt)
			if err == nil && now.Sub(at) >= 7*24*time.Hour {
				s.Pending = nil
				s.Observed = ""
			}
		}
		if s.Pending == nil {
			fingerprint := canaryFingerprint(root)
			s.LastCheckedAt = now.UTC().Format(time.RFC3339)
			s.Status = "watching"
			if fingerprint != s.Baseline {
				s.Status = "alert"
			}
			if fingerprint != s.Baseline && fingerprint != s.Observed {
				id := make([]byte, 16)
				if _, err := rand.Read(id); err != nil {
					return err
				}
				key := hex.EncodeToString(id)
				kind := "modified"
				if fingerprint == "missing" {
					kind = "deleted"
				}
				if fingerprint == "unsafe" {
					kind = "unsafe"
				}
				event := Event{SchemaVersion: 1, EventID: "canary_" + key, ObservedAt: now.UTC().Format(TimeLayout), Kind: "behavior_alert", Severity: "medium", Detector: "nexal_canary_" + kind, DetectorVersion: "2", OriginAssessment: "unknown", EvidenceRef: "canary_" + key}
				s.Pending = &event
			}
			s.Observed = fingerprint
			if err := saveCanary(root, s); err != nil {
				return err
			}
		}
		if s.Pending != nil {
			if report == nil {
				return errors.New("canary reporter unavailable")
			}
			if err := report(ctx, *s.Pending); err != nil {
				return err
			}
			s.Pending = nil
			return saveCanary(root, s)
		}
		return nil
	})
}

// Location deliberately accepts a configuration path only at the CLI boundary.
func CanaryForConfig(configPath string) Canary { return Canary{Directory: filepath.Dir(configPath)} }
