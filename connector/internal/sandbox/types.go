// Package sandbox runs throwaway Linux virtual machines on this Mac on behalf of
// the coordinator: it receives create/reset/delete tasks, downloads and verifies
// a cloud image, clones a per-sandbox disk, builds a cloud-init seed, asks a
// Hypervisor to boot the VM, and reports state back. Teardown is ordered and
// always removes the disk and the seed.
//
// The package is standard library only (no cgo). The hypervisor itself lives in a
// separate helper process (see vm.go), so a connector restart never kills a VM.
package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
)

// Kind is what the coordinator wants done with a sandbox.
type Kind string

const (
	KindCreate Kind = "create"
	KindReset  Kind = "reset"
	KindDelete Kind = "delete"
)

// State is a sandbox lifecycle state; the values are the wire names.
type State string

const (
	StateProvisioning State = "provisioning"
	StateRunning      State = "running"
	StateStopping     State = "stopping"
	StateDeleted      State = "deleted"
	StateFailed       State = "failed"
)

// Image describes the base image a create/reset task boots from.
type Image struct {
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Arch      string `json:"arch"`
	CloudInit bool   `json:"cloudInit"`
}

// Size is the requested virtual hardware.
type Size struct {
	CPUs     int `json:"cpus"`
	MemoryMB int `json:"memoryMB"`
	DiskGB   int `json:"diskGB"`
}

// Task is one unit of work from GET /api/v2/hosts/:id/sandbox-tasks.
// It carries secrets (SetupKey, VNCPassword): it must never be logged or
// formatted with %v. String and LogValue below redact it.
type Task struct {
	TaskID        string     `json:"taskId"`
	SandboxID     string     `json:"sandboxId"`
	Kind          Kind       `json:"kind"`
	Image         Image      `json:"image"`
	Size          Size       `json:"size"`
	SetupKey      string     `json:"setupKey"`
	VNCPassword   string     `json:"vncPassword"`
	SSHPublicKeys []string   `json:"sshPublicKeys"`
	Hostname      string     `json:"hostname"`
	Desktop       bool       `json:"desktop,omitempty"`
	ExpiresAt     *time.Time `json:"expiresAt"`
	Reach         string     `json:"reach"`
}

// String implements fmt.Stringer without any secret.
func (t Task) String() string {
	return fmt.Sprintf("sandbox.Task{task=%s sandbox=%s kind=%s}", t.TaskID, t.SandboxID, t.Kind)
}

// LogValue implements slog.LogValuer without any secret.
func (t Task) LogValue() slog.Value {
	return slog.GroupValue(slog.String("task", t.TaskID), slog.String("sandbox", t.SandboxID),
		slog.String("kind", string(t.Kind)))
}

// TasksResponse is the body of the sandbox-tasks endpoint.
type TasksResponse struct {
	Tasks []Task `json:"tasks"`
}

// StateReport is the body of POST /api/v2/hosts/:id/sandbox-state.
type StateReport struct {
	SandboxID          string `json:"sandboxId"`
	State              State  `json:"state"`
	MeshIP             string `json:"meshIp,omitempty"`
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`
	Error              string `json:"error,omitempty"`
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidID reports whether s is safe to use as an identifier and as a path
// component (no separators, no dots).
func ValidID(s string) bool { return idPattern.MatchString(s) }

var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidHostname reports whether s is a single DNS label.
func ValidHostname(s string) bool { return hostnamePattern.MatchString(s) }

// ValidateTask checks the fields common to every task kind. Create and reset
// are validated further by ValidateBoot.
func ValidateTask(t Task) error {
	if !ValidID(t.TaskID) {
		return fmt.Errorf("invalid taskId")
	}
	if !ValidID(t.SandboxID) {
		return fmt.Errorf("invalid sandboxId")
	}
	switch t.Kind {
	case KindCreate, KindReset, KindDelete:
	default:
		return fmt.Errorf("unknown task kind %q", string(t.Kind))
	}
	return nil
}

// Size limits. They bound what a task can ask for before the per-Mac caps apply.
const (
	maxCPUs     = 64
	minMemoryMB = 256
	maxMemoryMB = 256 * 1024
	maxDiskGB   = 1024
)

// ValidateSize rejects nonsensical hardware requests.
func ValidateSize(s Size) error {
	switch {
	case s.CPUs < 1 || s.CPUs > maxCPUs:
		return fmt.Errorf("cpus must be 1..%d", maxCPUs)
	case s.MemoryMB < minMemoryMB || s.MemoryMB > maxMemoryMB:
		return fmt.Errorf("memoryMB must be %d..%d", minMemoryMB, maxMemoryMB)
	case s.DiskGB < 1 || s.DiskGB > maxDiskGB:
		return fmt.Errorf("diskGB must be 1..%d", maxDiskGB)
	}
	return nil
}

// ValidateBoot checks a create/reset task's boot inputs.
func ValidateBoot(t Task) error {
	if err := ValidateSize(t.Size); err != nil {
		return err
	}
	if !ValidHostname(t.Hostname) {
		return fmt.Errorf("invalid hostname")
	}
	if !validSecret(t.SetupKey) {
		return fmt.Errorf("invalid setup key")
	}
	if t.VNCPassword != "" && !validSecret(t.VNCPassword) {
		return fmt.Errorf("invalid vnc password")
	}
	if len(t.SSHPublicKeys) > 64 {
		return fmt.Errorf("too many ssh keys")
	}
	for _, k := range t.SSHPublicKeys {
		if !validSSHPublicKey(k) {
			return fmt.Errorf("invalid ssh public key")
		}
	}
	return nil
}

var secretPattern = regexp.MustCompile(`^[A-Za-z0-9._~+=-]{1,256}$`)

// validSecret accepts the character set setup keys and generated passwords use
// and nothing that could break out of a quoted file value.
func validSecret(s string) bool { return secretPattern.MatchString(s) }

var sshKeyTypes = []string{"ssh-ed25519 ", "ssh-rsa ", "ecdsa-sha2-nistp256 ", "ecdsa-sha2-nistp384 ",
	"ecdsa-sha2-nistp521 ", "sk-ssh-ed25519@openssh.com ", "sk-ecdsa-sha2-nistp256@openssh.com "}

// validSSHPublicKey accepts a single-line OpenSSH public key.
func validSSHPublicKey(k string) bool {
	if len(k) == 0 || len(k) > 8192 || strings.ContainsAny(k, "\r\n\x00") {
		return false
	}
	for _, p := range sshKeyTypes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// Coordinator is the slice of the authenticated coordinator client this package
// needs. The agent's existing HTTP client implements it (see the adapter note in
// the work-package report); the package never builds its own authenticated client.
type Coordinator interface {
	// SandboxTasks is GET /api/v2/hosts/{hostID}/sandbox-tasks.
	SandboxTasks(ctx context.Context, hostID string) ([]Task, error)
	// ReportSandboxState is POST /api/v2/hosts/{hostID}/sandbox-state.
	ReportSandboxState(ctx context.Context, hostID string, r StateReport) error
}
