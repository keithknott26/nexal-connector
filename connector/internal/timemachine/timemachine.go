// Package timemachine models the paid, coordinator-controlled Time Machine
// destination exposed by a connector. It deliberately does not mount JuiceFS,
// fetch object-store credentials, or start an SMB daemon. Those operations need
// an installed privileged helper and an operator-provisioned JuiceFS mount.
package timemachine

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	FeatureName           = "time_machine_destination"
	BackendJuiceFS        = "juicefs_r2"
	MinQuotaBytes  uint64 = 50 << 30
)

// Config is non-secret desired state returned by the coordinator. Object-store
// keys and JuiceFS credentials must never be present in this document.
type Config struct {
	Enabled    bool     `json:"enabled"`
	Entitled   bool     `json:"entitled"`
	Revision   uint64   `json:"revision"`
	ShareName  string   `json:"shareName,omitempty"`
	MountPath  string   `json:"mountPath,omitempty"`
	Backend    string   `json:"backend,omitempty"`
	QuotaBytes uint64   `json:"quotaBytes,omitempty"`
	MeshCIDRs  []string `json:"meshCidrs,omitempty"`
	Advertise  bool     `json:"advertise"`
}

func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if !c.Entitled {
		return errors.New("Time Machine is enabled without an active paid entitlement")
	}
	if c.Revision == 0 {
		return errors.New("Time Machine configuration revision is required")
	}
	if c.Backend != BackendJuiceFS {
		return errors.New("Time Machine backend must be an explicitly provisioned JuiceFS R2 mount")
	}
	if err := validateName(c.ShareName); err != nil {
		return err
	}
	if !filepath.IsAbs(c.MountPath) || filepath.Clean(c.MountPath) != c.MountPath || c.MountPath == "/" {
		return errors.New("Time Machine mountPath must be an absolute, clean, dedicated directory")
	}
	if c.QuotaBytes < MinQuotaBytes {
		return fmt.Errorf("Time Machine quota must be at least %d GiB", MinQuotaBytes>>30)
	}
	if len(c.MeshCIDRs) == 0 || len(c.MeshCIDRs) > 16 {
		return errors.New("Time Machine requires 1-16 private mesh CIDRs")
	}
	for _, raw := range c.MeshCIDRs {
		p, err := netip.ParsePrefix(raw)
		if err != nil || p != p.Masked() || !meshAddress(p.Addr()) {
			return fmt.Errorf("Time Machine mesh CIDR %q must be a canonical private prefix", raw)
		}
	}
	return nil
}

func meshAddress(a netip.Addr) bool {
	if a.IsPrivate() {
		return true
	}
	// RFC 6598 shared address space is commonly allocated to overlay meshes. It
	// is not globally routable, even though netip correctly does not call it RFC
	// 1918 "private" space.
	cgnat := netip.MustParsePrefix("100.64.0.0/10")
	return a.Is4() && cgnat.Contains(a)
}

func validateName(s string) error {
	if s == "" || len(s) > 32 || strings.HasPrefix(s, "-") {
		return errors.New("invalid Time Machine share name")
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return errors.New("Time Machine share name may contain only letters, digits, dash and underscore")
		}
	}
	return nil
}

// Observation is local evidence. BackendMounted must come from the OS mount
// table, not merely from MountPath existing.
type Observation struct {
	Platform       string
	BackendMounted bool
	BackendType    string
	FreeBytes      uint64
	SMBConfigured  bool
	SMBHealthy     bool
	Advertised     bool
	AdminAvailable bool
	LastErrorCode  string
}

type Status struct {
	Feature       string `json:"feature"`
	State         string `json:"state"`
	Enabled       bool   `json:"enabled"`
	Entitled      bool   `json:"entitled"`
	Revision      uint64 `json:"revision,omitempty"`
	CapacityBytes uint64 `json:"capacityBytes,omitempty"`
	FreeBytes     uint64 `json:"freeBytes,omitempty"`
	Backend       string `json:"backend,omitempty"`
	ShareName     string `json:"shareName,omitempty"`
	Advertised    bool   `json:"advertised"`
	DetailCode    string `json:"detailCode,omitempty"`
}

// Evaluate never reports ready from configuration alone.
func Evaluate(c Config, o Observation) Status {
	s := Status{Feature: FeatureName, Enabled: c.Enabled, Entitled: c.Entitled, Revision: c.Revision,
		CapacityBytes: c.QuotaBytes, FreeBytes: o.FreeBytes, Backend: c.Backend, ShareName: c.ShareName, Advertised: o.Advertised}
	if !c.Enabled {
		s.State = "disabled"
		return s
	}
	if !c.Entitled {
		s.State, s.DetailCode = "blocked", "paid_entitlement_required"
		return s
	}
	if err := c.Validate(); err != nil {
		s.State, s.DetailCode = "blocked", "invalid_coordinator_config"
		return s
	}
	if o.Platform != "darwin" && o.Platform != "linux" {
		s.State, s.DetailCode = "unsupported", "unsupported_platform"
		return s
	}
	if !o.BackendMounted || o.BackendType != BackendJuiceFS {
		s.State, s.DetailCode = "blocked", "juicefs_mount_required"
		return s
	}
	if o.FreeBytes < MinQuotaBytes {
		s.State, s.DetailCode = "blocked", "insufficient_backend_space"
		return s
	}
	if !o.AdminAvailable {
		s.State, s.DetailCode = "action_required", "administrator_approval_required"
		return s
	}
	if !o.SMBConfigured {
		s.State, s.DetailCode = "configuring", "smb_configuration_pending"
		return s
	}
	if !o.SMBHealthy {
		s.State, s.DetailCode = "degraded", first(o.LastErrorCode, "smb_health_check_failed")
		return s
	}
	if c.Advertise && !o.Advertised {
		s.State, s.DetailCode = "degraded", "bonjour_advertisement_missing"
		return s
	}
	s.State = "ready"
	return s
}

func first(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func Platform() string { return runtime.GOOS }
