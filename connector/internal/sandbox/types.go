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
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
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
	// KindVNCPassword sets a one-time VNC password in a running VM (v2 contract).
	KindVNCPassword Kind = "vnc-password"
	// KindRejoin carries a fresh one-use mesh key for a sandbox whose mesh peer was
	// dropped (the Mac slept, or the VM/container was re-created). The runner asks
	// for it with StateReport.NeedsKey.
	KindRejoin Kind = "rejoin"
	// KindStop shuts a persistent VM down keeping its disk; KindStart boots it again from that disk.
	// Both are the owner's remote twins of the Mac app's local Stop and Start.
	KindStop  Kind = "stop"
	KindStart Kind = "start"
)

// SandboxKind is what a sandbox is: a full VM or a dev container.
type SandboxKind string

const (
	SandboxVM           SandboxKind = "vm"
	SandboxDevcontainer SandboxKind = "devcontainer"
)

// Lifecycle is persistent (disk and identity kept, no expiry) or ephemeral
// (wiped on every restart or stop, expires).
type Lifecycle string

const (
	LifecyclePersistent Lifecycle = "persistent"
	LifecycleEphemeral  Lifecycle = "ephemeral"
)

// Devcontainer is the dev-container payload: exactly one of the three fields.
type Devcontainer struct {
	RepoURL  string `json:"repoUrl,omitempty"`
	Template string `json:"template,omitempty"`
	JSON     string `json:"json,omitempty"`
}

// State is a sandbox lifecycle state; the values are the wire names.
type State string

const (
	StateProvisioning State = "provisioning"
	StateRunning      State = "running"
	StatePaused       State = "paused" // the host Mac is asleep or its lid is closed
	StateStopping     State = "stopping"
	StateStopped      State = "stopped" // persistent VM shut down with its disk kept; local only, never reported to the coordinator
	StateDeleted      State = "deleted"
	StateFailed       State = "failed"
)

// Image describes the base image a create/reset task boots from.
type Image struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// DigestStr is the coordinator's "digest": "sha256:<64hex>" or
	// "sha512:<128hex>". It wins over the legacy SHA256 field when present.
	DigestStr string `json:"digest,omitempty"`
	Arch      string `json:"arch"`
	CloudInit bool   `json:"cloudInit"`
	// CloudInitFlavor is the coordinator's spelling ("nocloud"); it implies CloudInit.
	CloudInitFlavor string `json:"cloudInitFlavor,omitempty"`
	// AppProfile is an application the seed installs on first boot ("" or "none"
	// for a plain image). An unknown profile is refused rather than booted
	// without its application: the coordinator may be ahead of this runner.
	AppProfile string `json:"appProfile,omitempty"`
}

// App profiles the seed can install. Adding one here is what lets the
// coordinator's catalog (migration 0086) offer it.
const (
	AppProfileNone          = "none"
	AppProfileHomeAssistant = "home-assistant"
	AppProfileJellyfin      = "jellyfin"
)

// KnownAppProfile reports whether the profile is one this runner can install.
// The empty string means a plain image, as does "none".
func KnownAppProfile(p string) bool {
	switch p {
	case "", AppProfileNone, AppProfileHomeAssistant, AppProfileJellyfin:
		return true
	}
	return false
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

	// v2 fields.
	ManagementURL  string        `json:"-"`
	SandboxKind    SandboxKind   `json:"sandboxKind,omitempty"`
	Lifecycle      Lifecycle     `json:"lifecycle,omitempty"`
	Devcontainer   *Devcontainer `json:"devcontainer,omitempty"`
	SSHCAPublicKey string        `json:"sshCaPublicKey,omitempty"`
	DriveMode      string        `json:"driveMode,omitempty"` // "rw" | "ro"
	DriveToken     string        `json:"driveToken,omitempty"`
	DriveURL       string        `json:"-"`                  // optional https endpoint for the shared drive (driveUrl)
	Password       string        `json:"password,omitempty"` // vnc-password task
	// Tenant is the coordinator's opaque per-tenant tag (16 hex) on tasks for a
	// managed host (neXal storage). The managed runner isolates containers by it.
	Tenant string `json:"tenant,omitempty"`
	// Managed is true on tasks for a managed host.
	Managed bool `json:"managed,omitempty"`

	// keepData is set by the runner (never the wire) when a re-create must keep
	// the persistent workspace's data.
	keepData bool
	// ackRejoin is set by the runner when a rejoin task is carried out as a
	// re-create: the final running report then acknowledges that task.
	ackRejoin bool
}

// taskWire is the coordinator's JSON shape (docs/sandboxes/API.md): it differs
// from Task (id vs taskId, resources, nested mesh).
type taskWire struct {
	ID                string        `json:"id"`
	TaskID            string        `json:"taskId"`
	Kind              Kind          `json:"kind"`
	SandboxID         string        `json:"sandboxId"`
	Hostname          string        `json:"hostname"`
	Desktop           bool          `json:"desktop"`
	Reach             string        `json:"reach"`
	ExpiresAt         *time.Time    `json:"expiresAt"`
	Image             Image         `json:"image"`
	Resources         *wireRes      `json:"resources"`
	Size              *Size         `json:"size"`
	Mesh              *wireMesh     `json:"mesh"`
	SetupKey          string        `json:"setupKey"`
	SSHAuthorizedKeys []string      `json:"sshAuthorizedKeys"`
	SSHPublicKeys     []string      `json:"sshPublicKeys"`
	VNCPassword       string        `json:"vncPassword"`
	Password          string        `json:"password"`
	SandboxKind       SandboxKind   `json:"sandboxKind"`
	Lifecycle         Lifecycle     `json:"lifecycle"`
	Persistent        bool          `json:"persistent"`
	Devcontainer      *Devcontainer `json:"devcontainer"`
	SSHCAPublicKey    string        `json:"sshCaPublicKey"`
	DriveMode         string        `json:"driveMode"`
	DriveToken        string        `json:"driveToken"`
	DriveURL          string        `json:"driveUrl"`
	Tenant            string        `json:"tenant"`
	Managed           bool          `json:"managed"`
}

type wireRes struct {
	CPU      int `json:"cpu"`
	MemoryMB int `json:"memoryMb"`
	DiskGB   int `json:"diskGb"`
}

type wireMesh struct {
	ManagementURL string `json:"managementUrl"`
	SetupKey      string `json:"setupKey"`
}

// UnmarshalJSON accepts the coordinator's wire shape. Unknown fields are ignored
// (the coordinator deploys before connectors update).
func (t *Task) UnmarshalJSON(b []byte) error {
	var w taskWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*t = Task{TaskID: w.ID, SandboxID: w.SandboxID, Kind: w.Kind, Image: w.Image, SetupKey: w.SetupKey,
		VNCPassword: w.VNCPassword, Hostname: w.Hostname, Desktop: w.Desktop, ExpiresAt: w.ExpiresAt,
		Reach: w.Reach, SandboxKind: w.SandboxKind, Lifecycle: w.Lifecycle, Devcontainer: w.Devcontainer,
		SSHCAPublicKey: w.SSHCAPublicKey, DriveMode: w.DriveMode, DriveToken: w.DriveToken, DriveURL: w.DriveURL, Password: w.Password,
		Tenant: w.Tenant, Managed: w.Managed}
	if t.TaskID == "" {
		t.TaskID = w.TaskID
	}
	if w.Size != nil {
		t.Size = *w.Size
	}
	if w.Resources != nil {
		t.Size = Size{CPUs: w.Resources.CPU, MemoryMB: w.Resources.MemoryMB, DiskGB: w.Resources.DiskGB}
	}
	if w.Mesh != nil {
		t.ManagementURL = w.Mesh.ManagementURL
		if w.Mesh.SetupKey != "" {
			t.SetupKey = w.Mesh.SetupKey
		}
	}
	t.SSHPublicKeys = w.SSHAuthorizedKeys
	if len(t.SSHPublicKeys) == 0 {
		t.SSHPublicKeys = w.SSHPublicKeys
	}
	if t.Image.CloudInitFlavor == "nocloud" {
		t.Image.CloudInit = true
	}
	if w.Persistent && t.Lifecycle == "" {
		t.Lifecycle = LifecyclePersistent
	}
	return nil
}

// IsPersistent reports whether the sandbox keeps its disk, identity and has no expiry.
func (t Task) IsPersistent() bool { return t.Lifecycle == LifecyclePersistent }

// IsDev reports whether the task is for a dev container.
func (t Task) IsDev() bool {
	return t.SandboxKind == SandboxDevcontainer || (t.SandboxKind == "" && t.Devcontainer != nil)
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
	SandboxID string `json:"sandboxId"`
	State     State  `json:"state"`
	// NeedsKey asks the coordinator for a fresh one-use mesh key (a rejoin task):
	// the peer was dropped while the Mac slept, or the sandbox was re-created.
	NeedsKey bool `json:"needsKey,omitempty"`
	// AckTaskID names a task (vnc-password) this report acknowledges. The
	// coordinator's canonical name is ackTaskId (it also accepts ackedTask).
	AckTaskID          string `json:"ackTaskId,omitempty"`
	MeshIP             string `json:"meshIp,omitempty"`
	LanIP              string `json:"lanIp,omitempty"` // a bridged VM's home-network address (nexal-vmnet)
	HostKeyFingerprint string `json:"hostKeyFingerprint,omitempty"`
	// HostKey is the guest's ssh-ed25519 host public key ("ssh-ed25519 AAAA...",
	// no comment); the coordinator pins it for connect responses.
	HostKey string `json:"hostKey,omitempty"`
	Error   string `json:"error,omitempty"`
	// Step and Percent describe provisioning progress (steps: check, download,
	// convert, disk, seed, boot, join), and app setup on a running host
	// (app-packages, app-download, app-start, app-failed).
	Step    string `json:"step,omitempty"`
	Percent int    `json:"percent,omitempty"`
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
	case KindCreate, KindReset, KindDelete, KindVNCPassword, KindRejoin, KindStop, KindStart:
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
	dev := t.IsDev()
	if !dev || t.Size != (Size{}) {
		if err := ValidateSize(t.Size); err != nil {
			return err
		}
	}
	if err := validateV2(t); err != nil {
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

// validateV2 checks the lifecycle, access and dev-container fields.
func validateV2(t Task) error {
	switch t.Lifecycle {
	case "", LifecyclePersistent, LifecycleEphemeral:
	default:
		return fmt.Errorf("unknown lifecycle")
	}
	switch t.SandboxKind {
	case "", SandboxVM, SandboxDevcontainer:
	default:
		return fmt.Errorf("unknown sandbox kind")
	}
	switch t.DriveMode {
	case "", "ro", "rw":
	default:
		return fmt.Errorf("invalid drive mode")
	}
	if t.DriveToken != "" && !validToken(t.DriveToken) {
		return fmt.Errorf("invalid drive token")
	}
	if t.SSHCAPublicKey != "" && !validCAKey(t.SSHCAPublicKey) {
		return fmt.Errorf("invalid ssh ca public key")
	}
	if t.ManagementURL != "" && !validHTTPSURL(t.ManagementURL) {
		return fmt.Errorf("invalid management url")
	}
	if t.DriveURL != "" && !validHTTPSURL(t.DriveURL) {
		return fmt.Errorf("invalid drive url")
	}
	if t.Tenant != "" && !ValidTenantTag(t.Tenant) {
		return fmt.Errorf("invalid tenant tag")
	}
	if !KnownAppProfile(t.Image.AppProfile) {
		return fmt.Errorf("unknown app profile")
	}
	if t.IsDev() {
		return ValidateDevcontainer(t.Devcontainer)
	}
	return nil
}

var tenantTagPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)

// ValidTenantTag accepts the coordinator's per-tenant tag (16 lowercase hex).
// It is used in docker network, label and cgroup slice names, so nothing else
// is accepted.
func ValidTenantTag(s string) bool { return tenantTagPattern.MatchString(s) }

// validHTTPSURL accepts an https URL that is also safe inside a quoted env value.
func validHTTPSURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && len(s) <= 2048 &&
		!strings.ContainsAny(s, " \t\r\n\x00'\"\\$`")
}

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~+=/-]+$`)

// validToken accepts scoped-token characters (JWT/base64url-like) and nothing
// that could break out of a quoted file value.
func validToken(s string) bool { return len(s) <= 2048 && tokenPattern.MatchString(s) }

// validCAKey accepts a single-line OpenSSH public key that is also safe inside a
// single-quoted shell/env value.
func validCAKey(k string) bool {
	return validSSHPublicKey(k) && !strings.ContainsAny(k, "'\"\\$`")
}

var templatePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

const maxDevcontainerJSON = 64 << 10

// ValidateDevcontainer checks the dev-container payload: exactly one of repoUrl,
// template or inline json.
func ValidateDevcontainer(d *Devcontainer) error {
	if d == nil {
		return fmt.Errorf("devcontainer payload required")
	}
	n := 0
	if d.RepoURL != "" {
		n++
		u, err := url.Parse(d.RepoURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || len(d.RepoURL) > 2048 ||
			strings.ContainsAny(d.RepoURL, " \t\r\n\x00'\"\\$`") {
			return fmt.Errorf("invalid devcontainer repoUrl")
		}
	}
	if d.Template != "" {
		n++
		if !templatePattern.MatchString(d.Template) {
			return fmt.Errorf("invalid devcontainer template")
		}
	}
	if d.JSON != "" {
		n++
		if len(d.JSON) > maxDevcontainerJSON || !json.Valid([]byte(d.JSON)) {
			return fmt.Errorf("invalid devcontainer json")
		}
	}
	if n != 1 {
		return fmt.Errorf("devcontainer needs exactly one of repoUrl, template, json")
	}
	return nil
}
