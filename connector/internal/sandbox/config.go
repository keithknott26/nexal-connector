package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// HostingConfig is the owner's opt-in for hosting throwaway sandboxes on this
// Mac. The Mac app writes it; the connector only reads it (see README.md).
//
//	~/Library/Application Support/Nexal/sandbox-hosting.json
//	{"enabled": true, "maxSandboxes": 5, "placement": "members"}
//
// A missing file means "opted in" with the defaults (allow by default). An invalid file also means "not opted in"
// (fail closed), and is reported through the error from LoadHostingConfig.
type HostingConfig struct {
	Enabled      bool   `json:"enabled"`
	MaxSandboxes int    `json:"maxSandboxes"`
	Placement    string `json:"placement"` // "members" | "owner"
}

// Placement values.
const (
	PlacementMembers = "members"
	PlacementOwner   = "owner"
)

// DefaultHostingConfigPath is ~/Library/Application Support/Nexal/sandbox-hosting.json.
func DefaultHostingConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Nexal", "sandbox-hosting.json"), nil
}

// Normalize validates and fills defaults: maxSandboxes 1..10 (default 5),
// placement members|owner (default members).
func (c HostingConfig) Normalize() (HostingConfig, error) {
	if c.MaxSandboxes == 0 {
		c.MaxSandboxes = 5
	}
	if c.MaxSandboxes < 1 || c.MaxSandboxes > 10 {
		return HostingConfig{}, fmt.Errorf("maxSandboxes must be 1..10")
	}
	switch c.Placement {
	case "any": // tolerated spelling from older Mac apps
		c.Placement = PlacementMembers
	case "mine":
		c.Placement = PlacementOwner
	}
	switch c.Placement {
	case "":
		c.Placement = PlacementMembers
	case PlacementMembers, PlacementOwner:
	default:
		return HostingConfig{}, fmt.Errorf("placement must be %q or %q", PlacementMembers, PlacementOwner)
	}
	return c, nil
}

// ParseHostingConfig decodes and normalizes the file body. Unknown fields are
// ignored so the Mac app can add its own.
func ParseHostingConfig(b []byte) (HostingConfig, error) {
	var c HostingConfig
	if len(b) > 16<<10 {
		return HostingConfig{}, errors.New("sandbox hosting config too large")
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return HostingConfig{}, errors.New("sandbox hosting config is not valid JSON")
	}
	return c.Normalize()
}

// LoadHostingConfig reads the file. A missing file returns the default (enabled)
// config and a nil error; an invalid file returns a disabled config and an error.
func LoadHostingConfig(path string) (HostingConfig, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// No file yet: hosting is on by default. The owner turns it off in the Mac app.
		return HostingConfig{Enabled: true, MaxSandboxes: 5, Placement: PlacementMembers}, nil
	}
	if err != nil {
		return HostingConfig{MaxSandboxes: 5, Placement: PlacementMembers}, err
	}
	c, err := ParseHostingConfig(b)
	if err != nil {
		return HostingConfig{MaxSandboxes: 5, Placement: PlacementMembers}, err
	}
	return c, nil
}

// Caps turns the owner's choice into admission caps. Battery no longer refuses
// placement: sandboxes sleep with the Mac (suspend-with-host, §11a), so the
// owner's power state is reported instead (sandbox-hosting-state).
func (c HostingConfig) Caps() Caps {
	k := DefaultCaps()
	k.Enabled = c.Enabled
	k.MaxSandboxes = c.MaxSandboxes
	k.AllowOnBattery = true
	return k.Normalized()
}

// configWatcher re-reads the config file when its mtime or size changes.
type configWatcher struct {
	path string
	mod  time.Time
	size int64
	seen bool
}

// Changed reloads the file if it changed since the last call (the first call
// always reports a change). err is non-nil for an unreadable or invalid file; the
// returned config is then the disabled default.
func (w *configWatcher) Changed() (cfg HostingConfig, changed bool, err error) {
	fi, serr := os.Stat(w.path)
	switch {
	case serr != nil && errors.Is(serr, os.ErrNotExist):
		if w.seen && w.size == -1 {
			return HostingConfig{}, false, nil
		}
		w.seen, w.size, w.mod = true, -1, time.Time{}
		cfg, err = LoadHostingConfig(w.path)
		return cfg, true, err
	case serr != nil:
		cfg, err = LoadHostingConfig(w.path)
		return cfg, false, err
	}
	if w.seen && fi.ModTime().Equal(w.mod) && fi.Size() == w.size {
		return HostingConfig{}, false, nil
	}
	w.seen, w.mod, w.size = true, fi.ModTime(), fi.Size()
	cfg, err = LoadHostingConfig(w.path)
	return cfg, true, err
}
