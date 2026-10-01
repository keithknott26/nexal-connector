package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Spec is everything the VM host process needs to boot one guest. It is written
// as JSON to a file and handed to `nexal-vmhost run --spec <file>`. It contains
// paths only; secrets are inside the seed image.
//
// The nexal-vmhost contract (implemented elsewhere; Virtualization.framework
// needs the com.apple.security.virtualization entitlement and, in practice, cgo
// or Swift, which is why it is a separate binary):
//
//	nexal-vmhost run  --spec <file>     boot the VM (UEFI), run until the guest
//	                                    powers off, then exit 0; serve a control
//	                                    socket at Spec.ControlSocket
//	nexal-vmhost stop --control <sock>  request an ACPI power-button shutdown
//	                                    and return immediately
//
// SeedPath may be empty (a persistent VM restarted after first boot has no seed).
//
// Devices: virtio block (Disk, read-write), virtio block (Seed, read-only),
// NAT network, virtio entropy, serial console appended to ConsoleLog, and a
// virtio GPU when Desktop is set.
type Spec struct {
	SandboxID     string `json:"sandboxId"`
	Hostname      string `json:"hostname"`
	CPUs          int    `json:"cpus"`
	MemoryMB      int    `json:"memoryMB"`
	DiskPath      string `json:"diskPath"`
	SeedPath      string `json:"seedPath"`
	ConsoleLog    string `json:"consoleLog"`
	ControlSocket string `json:"controlSocket"`
	// GuestSocket is a unix socket nexal-vmhost serves and bridges to a second
	// virtio console port in the guest (see guest.go). Empty disables it.
	GuestSocket string `json:"guestSocket,omitempty"`
	Desktop     bool   `json:"desktop"`
	// KeepAwake asks the host process to hold a power assertion while the VM runs.
	// The runner always sends false: sandboxes sleep with the Mac (suspend-with-host).
	KeepAwake bool `json:"keepAwake"`
}

// Handle identifies a started VM. It is persisted so a restarted connector can
// find the VM again.
type Handle struct {
	SandboxID string `json:"sandboxId"`
	Label     string `json:"label"` // launchd label
	Spec      string `json:"spec"`  // path of the spec file
	Control   string `json:"control"`
	Plist     string `json:"plist"`
	Guest     string `json:"guest,omitempty"` // guest channel socket
}

// Hypervisor starts and stops guests. Stop requests a graceful (ACPI) shutdown
// and returns once it is requested, not once the VM is gone; the manager waits
// and then calls Kill. Alive is the extra method the manager needs to wait.
type Hypervisor interface {
	Start(ctx context.Context, s Spec) (Handle, error)
	Stop(ctx context.Context, h Handle) error
	Kill(ctx context.Context, h Handle) error
	Alive(ctx context.Context, h Handle) (bool, error)
}

// cmdRunner runs an external command; replaceable for tests.
type cmdRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// vzHypervisor is the Virtualization.framework backend. It does no
// virtualization itself: it writes a spec, installs a per-VM launchd job that
// runs the future nexal-vmhost binary, and supervises it with launchctl. One job
// per VM means a connector restart or update never kills a guest.
type vzHypervisor struct {
	vmhost string // path to nexal-vmhost
	runDir string // where spec files and plists live
	uid    int
	run    cmdRunner
	// sockDir holds the control and guest sockets. unix socket paths are limited
	// to ~104 bytes on macOS, which "~/Library/Application Support/..." can
	// exceed, so it may point at a short directory. Empty means runDir.
	sockDir string
	// search lists where to look for nexal-vmhost when v.vmhost is missing. It is
	// evaluated on every Start, so installing the helper later needs no restart.
	search func() []string
}

// VMHostCandidates is every place nexal-vmhost is looked for, in order: next to
// the connector (the app bundle's Helpers folder), the per-user install location,
// the installed app, then common tool directories. base is the neXal data
// directory (~/Library/Application Support/Nexal).
func VMHostCandidates(base string) []string {
	var c []string
	if exe, err := os.Executable(); err == nil {
		c = append(c, filepath.Join(filepath.Dir(exe), "nexal-vmhost"))
	}
	c = append(c, filepath.Join(base, "bin", "nexal-vmhost"))
	if home, err := os.UserHomeDir(); err == nil {
		c = append(c, filepath.Join(home, "Applications", "neXal-Connector.app", "Contents", "Helpers", "nexal-vmhost"))
	}
	return append(c,
		"/Applications/neXal-Connector.app/Contents/Helpers/nexal-vmhost",
		"/opt/homebrew/bin/nexal-vmhost", "/usr/local/bin/nexal-vmhost")
}

// ResolveVMHost returns the first existing executable among preferred (if set)
// and candidates, or "" and the list that was searched.
func ResolveVMHost(preferred string, candidates []string) (string, []string) {
	all := candidates
	if preferred != "" {
		all = append([]string{preferred}, candidates...)
	}
	for _, p := range all {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, all
		}
	}
	return "", all
}

// NewVZHypervisorWithSockets is NewVZHypervisor with a separate (short) directory
// for the unix sockets. sockDir is created 0700.
func NewVZHypervisorWithSockets(vmhostPath, runDir, sockDir string) Hypervisor {
	base := filepath.Dir(filepath.Dir(runDir)) // runDir is <base>/sandboxes/run
	return &vzHypervisor{vmhost: vmhostPath, runDir: runDir, uid: os.Getuid(), run: execRunner, sockDir: sockDir,
		search: func() []string { return VMHostCandidates(base) }}
}

// NewVZHypervisor returns the launchd-backed hypervisor. vmhostPath is the
// nexal-vmhost binary; runDir holds per-VM spec and plist files (created 0700).
func NewVZHypervisor(vmhostPath, runDir string) Hypervisor {
	return &vzHypervisor{vmhost: vmhostPath, runDir: runDir, uid: os.Getuid(), run: execRunner}
}

const launchdPrefix = "systems.nexal.vmhost."

func (v *vzHypervisor) searched() []string {
	_, all := ResolveVMHost(v.vmhost, v.candidates())
	return all
}
func (v *vzHypervisor) candidates() []string {
	if v.search == nil {
		return nil
	}
	return v.search()
}
func (v *vzHypervisor) resolveHost() (string, bool) {
	p, _ := ResolveVMHost(v.vmhost, v.candidates())
	return p, p != ""
}

func (v *vzHypervisor) domain() string { return fmt.Sprintf("gui/%d", v.uid) }

func (v *vzHypervisor) Start(ctx context.Context, s Spec) (Handle, error) {
	if !ValidID(s.SandboxID) {
		return Handle{}, errors.New("invalid sandbox id")
	}
	if p, ok := v.resolveHost(); ok {
		v.vmhost = p
	} else {
		return Handle{}, fmt.Errorf("nexal-vmhost is not installed (looked in: %s). Reinstall neXal@home 0.4.0 or later, or set NEXAL_VMHOST to the helper's path", strings.Join(v.searched(), ", "))
	}
	if err := os.MkdirAll(v.runDir, 0o700); err != nil {
		return Handle{}, err
	}
	sdir := v.runDir
	if v.sockDir != "" {
		sdir = v.sockDir
		if err := os.MkdirAll(sdir, 0o700); err != nil {
			return Handle{}, err
		}
	}
	label := launchdPrefix + s.SandboxID
	h := Handle{
		SandboxID: s.SandboxID,
		Label:     label,
		Spec:      filepath.Join(v.runDir, s.SandboxID+".spec.json"),
		Control:   filepath.Join(sdir, s.SandboxID+".sock"),
		Guest:     filepath.Join(sdir, s.SandboxID+".guest.sock"),
		Plist:     filepath.Join(v.runDir, label+".plist"),
	}
	s.ControlSocket = h.Control
	s.GuestSocket = h.Guest
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return Handle{}, err
	}
	if err := os.WriteFile(h.Spec, b, 0o600); err != nil {
		return Handle{}, err
	}
	if err := os.WriteFile(h.Plist, []byte(RenderLaunchdPlist(label, v.vmhost, h.Spec, s.ConsoleLog+".host")), 0o600); err != nil {
		return Handle{}, err
	}
	if out, err := v.run(ctx, "/bin/launchctl", "bootstrap", v.domain(), h.Plist); err != nil {
		_ = os.Remove(h.Plist)
		_ = os.Remove(h.Spec)
		return Handle{}, fmt.Errorf("launchctl bootstrap failed: %v: %s", err, trimOutput(out))
	}
	return h, nil
}

func (v *vzHypervisor) Stop(ctx context.Context, h Handle) error {
	if h.Control == "" {
		return errors.New("no control socket")
	}
	if out, err := v.run(ctx, v.vmhost, "stop", "--control", h.Control); err != nil {
		return fmt.Errorf("stop request failed: %v: %s", err, trimOutput(out))
	}
	return nil
}

func (v *vzHypervisor) Kill(ctx context.Context, h Handle) error {
	if h.Label == "" {
		return nil
	}
	out, err := v.run(ctx, "/bin/launchctl", "bootout", v.domain()+"/"+h.Label)
	// An already-unloaded job is the desired end state, not a failure.
	alive, aerr := v.Alive(ctx, h)
	if aerr == nil && !alive {
		v.cleanupFiles(h)
		return nil
	}
	if err != nil {
		return fmt.Errorf("launchctl bootout failed: %v: %s", err, trimOutput(out))
	}
	v.cleanupFiles(h)
	return nil
}

func (v *vzHypervisor) Alive(ctx context.Context, h Handle) (bool, error) {
	if h.Label == "" {
		return false, nil
	}
	out, err := v.run(ctx, "/bin/launchctl", "print", v.domain()+"/"+h.Label)
	if err != nil {
		// launchctl print exits non-zero when the service is not loaded.
		return false, nil
	}
	return bytes.Contains(out, []byte("state = running")), nil
}

func (v *vzHypervisor) cleanupFiles(h Handle) {
	for _, p := range []string{h.Plist, h.Spec, h.Control, h.Guest} {
		if p != "" {
			_ = os.Remove(p)
		}
	}
}

// RenderLaunchdPlist renders the per-VM launchd job. RunAtLoad starts it at
// bootstrap; KeepAlive is off so a guest that powers off is not restarted. It is
// pure.
func RenderLaunchdPlist(label, vmhost, specPath, logPath string) string {
	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + esc(label) + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range []string{vmhost, "run", "--spec", specPath} {
		b.WriteString("\t\t<string>" + esc(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<false/>\n")
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Interactive</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + esc(logPath) + "</string>\n")
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + esc(logPath) + "</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}
