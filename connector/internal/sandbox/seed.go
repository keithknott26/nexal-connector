package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SeedParams is everything the per-sandbox cloud-init seed carries. All of it is
// secret or per-sandbox; the image itself holds no secrets.
type SeedParams struct {
	SandboxID     string
	InstanceID    string // changes on reset so cloud-init re-runs
	Hostname      string
	SetupKey      string
	VNCPassword   string
	SSHPublicKeys []string
	Desktop       bool
	DriveWritable bool
	// v2: per-network SSH CA (installed as TrustedUserCAKeys), scoped drive token,
	// lifecycle, mesh management URL.
	SSHCAPublicKey string
	DriveToken     string
	Lifecycle      Lifecycle
	ManagementURL  string // rendered as NEXAL_MESH_URL (what nexal-first-boot reads)
	DriveURL       string // optional shared-drive endpoint, rendered as NEXAL_DRIVE_URL
}

// First-boot protocol. The guest's first-boot script (shipped in the pre-baked
// images, and installed by this seed on others) prints one line to the serial
// console once the mesh has joined and sshd has its host keys:
//
//	NEXAL-FIRSTBOOT {"meshIp":"100.x.y.z","hostKeyFingerprint":"SHA256:...","hostKey":"ssh-ed25519 AAAA..."}
//
// or, on failure,
//
//	NEXAL-FIRSTBOOT-FAILED <short reason>
//
// The runner reads these from the VM's console log (ParseFirstBootLine) and
// reports them to the coordinator, then deletes the seed.
const (
	FirstBootPrefix       = "NEXAL-FIRSTBOOT "
	FirstBootFailedPrefix = "NEXAL-FIRSTBOOT-FAILED"
)

// ValidateSeed checks every value that is interpolated into the seed.
func ValidateSeed(p SeedParams) error {
	if !ValidID(p.SandboxID) {
		return errors.New("invalid sandbox id")
	}
	if !ValidID(p.InstanceID) {
		return errors.New("invalid instance id")
	}
	if !ValidHostname(p.Hostname) {
		return errors.New("invalid hostname")
	}
	if !validSecret(p.SetupKey) {
		return errors.New("invalid setup key")
	}
	if p.VNCPassword != "" && !validSecret(p.VNCPassword) {
		return errors.New("invalid vnc password")
	}
	for _, k := range p.SSHPublicKeys {
		if !validSSHPublicKey(k) {
			return errors.New("invalid ssh public key")
		}
	}
	if p.SSHCAPublicKey != "" && !validCAKey(p.SSHCAPublicKey) {
		return errors.New("invalid ssh ca public key")
	}
	if p.DriveToken != "" && !validToken(p.DriveToken) {
		return errors.New("invalid drive token")
	}
	switch p.Lifecycle {
	case "", LifecyclePersistent, LifecycleEphemeral:
	default:
		return errors.New("invalid lifecycle")
	}
	if p.DriveURL != "" && !validHTTPSURL(p.DriveURL) {
		return errors.New("invalid drive url")
	}
	if p.ManagementURL != "" && validateV2(Task{ManagementURL: p.ManagementURL}) != nil {
		return errors.New("invalid management url")
	}
	return nil
}

// yq quotes a string as a YAML scalar. JSON strings are valid YAML double-quoted
// scalars, so the encoder does the escaping.
func yq(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimRight(b.String(), "\n")
}

// RenderMetaData renders the NoCloud meta-data file.
func RenderMetaData(p SeedParams) string {
	return "instance-id: " + p.InstanceID + "\nlocal-hostname: " + p.Hostname + "\n"
}

// RenderUserData renders the NoCloud user-data file (#cloud-config). It sets up
// user nexal with sudo and the members' public keys, key-only SSH, the join
// environment (setup key, VNC password) in a root-only file, a drive mount unit,
// and runs the first-boot script.
//
// The mesh-only listen restriction cannot be written statically (the mesh
// address is unknown until the join), so the first-boot script enforces it from
// NEXAL_SSH_MESH_ONLY / NEXAL_VNC_MESH_ONLY; sshd here only gets the key-only
// hardening.
func RenderUserData(p SeedParams) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	b.WriteString("#cloud-config\n")
	w("hostname: %s\n", yq(p.Hostname))
	b.WriteString("manage_etc_hosts: true\n")
	b.WriteString("ssh_pwauth: false\n")
	b.WriteString("disable_root: true\n")
	b.WriteString("users:\n")
	b.WriteString("  - name: nexal\n")
	b.WriteString("    gecos: neXal throwaway host\n")
	b.WriteString("    shell: /bin/bash\n")
	b.WriteString("    lock_passwd: true\n")
	b.WriteString("    sudo: \"ALL=(ALL) NOPASSWD:ALL\"\n")
	if len(p.SSHPublicKeys) == 0 {
		b.WriteString("    ssh_authorized_keys: []\n")
	} else {
		b.WriteString("    ssh_authorized_keys:\n")
		for _, k := range p.SSHPublicKeys {
			w("      - %s\n", yq(k))
		}
	}
	b.WriteString("write_files:\n")
	// Secrets file: root-only, removed by the first-boot script once consumed.
	b.WriteString("  - path: /etc/nexal/first-boot.env\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0600\"\n")
	b.WriteString("    content: |\n")
	w("      NEXAL_SANDBOX_ID=%s\n", p.SandboxID)
	w("      NEXAL_HOSTNAME=%s\n", p.Hostname)
	w("      NEXAL_SETUP_KEY=%s\n", p.SetupKey)
	w("      NEXAL_VNC_PASSWORD=%s\n", p.VNCPassword)
	w("      NEXAL_DESKTOP=%s\n", boolDigit(p.Desktop))
	w("      NEXAL_DRIVE_WRITABLE=%s\n", boolDigit(p.DriveWritable))
	w("      NEXAL_DRIVE_MODE=%s\n", driveMode(p.DriveWritable))
	w("      NEXAL_DRIVE_TOKEN=%s\n", p.DriveToken)
	lc := p.Lifecycle
	if lc == "" {
		lc = LifecycleEphemeral
	}
	w("      NEXAL_LIFECYCLE=%s\n", string(lc))
	if p.SSHCAPublicKey != "" {
		w("      NEXAL_SSH_CA='%s'\n", p.SSHCAPublicKey)
	}
	if p.ManagementURL != "" {
		w("      NEXAL_MESH_URL=%s\n", p.ManagementURL)
	}
	if p.DriveURL != "" {
		w("      NEXAL_DRIVE_URL=%s\n", p.DriveURL)
	}
	b.WriteString("      NEXAL_SSH_MESH_ONLY=1\n")
	b.WriteString("      NEXAL_VNC_MESH_ONLY=1\n")
	b.WriteString("  - path: /etc/ssh/sshd_config.d/10-nexal.conf\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0644\"\n")
	b.WriteString("    content: |\n")
	b.WriteString("      PasswordAuthentication no\n")
	b.WriteString("      KbdInteractiveAuthentication no\n")
	b.WriteString("      PermitRootLogin no\n")
	b.WriteString("      AllowUsers nexal\n")
	if p.SSHCAPublicKey != "" {
		// Members connect with a 10-minute certificate (principal nexal) signed by the
		// per-network CA; no long-lived member key is distributed.
		b.WriteString("      TrustedUserCAKeys /etc/ssh/nexal_user_ca.pub\n")
		b.WriteString("  - path: /etc/ssh/nexal_user_ca.pub\n")
		b.WriteString("    owner: root:root\n")
		b.WriteString("    permissions: \"0644\"\n")
		b.WriteString("    content: |\n")
		w("      %s\n", p.SSHCAPublicKey)
	}
	if p.DriveToken != "" {
		// The scoped drive token outlives first boot (the mount service needs it);
		// it expires with the sandbox on the coordinator side.
		b.WriteString("  - path: /etc/nexal/drive.env\n")
		b.WriteString("    owner: root:root\n")
		b.WriteString("    permissions: \"0600\"\n")
		b.WriteString("    content: |\n")
		w("      NEXAL_DRIVE_TOKEN=%s\n", p.DriveToken)
	}
	b.WriteString("  - path: /etc/systemd/system/nexal-drive.service\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0644\"\n")
	b.WriteString("    content: |\n")
	b.WriteString("      [Unit]\n")
	b.WriteString("      Description=neXal shared drive (FUSE)\n")
	b.WriteString("      After=network-online.target nexal-mesh.service\n")
	b.WriteString("      Wants=network-online.target\n")
	b.WriteString("      ConditionPathExists=/usr/local/bin/nexal\n")
	b.WriteString("      [Service]\n")
	b.WriteString("      Type=simple\n")
	b.WriteString("      EnvironmentFile=-/etc/nexal/drive.env\n")
	if p.DriveWritable {
		b.WriteString("      ExecStart=/usr/local/bin/nexal drive mount --rw /mnt/nexal-drive\n")
	} else {
		b.WriteString("      ExecStart=/usr/local/bin/nexal drive mount --ro /mnt/nexal-drive\n")
	}
	b.WriteString("      ExecStop=/bin/umount -l /mnt/nexal-drive\n")
	b.WriteString("      Restart=on-failure\n")
	b.WriteString("      [Install]\n")
	b.WriteString("      WantedBy=multi-user.target\n")
	b.WriteString("runcmd:\n")
	b.WriteString("  - [ mkdir, -p, /mnt/nexal-drive ]\n")
	b.WriteString("  - [ sh, -c, \"systemctl restart ssh 2>/dev/null || systemctl restart sshd 2>/dev/null || true\" ]\n")
	// The first-boot script is part of the pre-baked image (it installs/starts
	// the mesh runtime with NEXAL_SETUP_KEY, sets the VNC password, binds sshd
	// and VNC to the mesh interface, enables nexal-drive, prints the
	// NEXAL-FIRSTBOOT line, then shreds first-boot.env).
	b.WriteString("  - [ sh, -c, \"if [ -x /usr/local/sbin/nexal-first-boot ]; then /usr/local/sbin/nexal-first-boot > /dev/console 2>&1; else echo 'NEXAL-FIRSTBOOT-FAILED no first-boot script in image' > /dev/console; fi\" ]\n")
	return b.String()
}

func driveMode(writable bool) string {
	if writable {
		return "rw"
	}
	return "ro"
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// WriteSeedDir writes user-data and meta-data into dir (created 0700).
func WriteSeedDir(dir string, p SeedParams) error {
	if err := ValidateSeed(p); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "user-data"), []byte(RenderUserData(p)), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta-data"), []byte(RenderMetaData(p)), 0o600)
}

// ISOBuilder packs a seed directory into an ISO9660 image labelled "cidata".
type ISOBuilder func(ctx context.Context, srcDir, outISO string) error

// HdiutilISO is the default ISOBuilder, using macOS's hdiutil.
func HdiutilISO(ctx context.Context, srcDir, outISO string) error {
	if !strings.HasSuffix(outISO, ".iso") {
		return errors.New("seed image path must end in .iso")
	}
	_ = os.Remove(outISO)
	out, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "makehybrid", "-iso", "-joliet",
		"-default-volume-name", "cidata", "-o", outISO, srcDir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("building seed image failed: %v: %s", err, trimOutput(out))
	}
	return nil
}

// RemoveSeed deletes the seed directory and image. Missing files are not an error.
func RemoveSeed(dir, iso string) error {
	var first error
	if dir != "" {
		if err := os.RemoveAll(dir); err != nil {
			first = err
		}
	}
	if iso != "" {
		if err := os.Remove(iso); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	return first
}

// FirstBoot is the guest's first-boot report.
type FirstBoot struct {
	MeshIP             string `json:"meshIp"`
	HostKeyFingerprint string `json:"hostKeyFingerprint"`
	// HostKey is the guest's ssh-ed25519 host public key, "ssh-ed25519 AAAA..."
	// without a comment (contents of ssh_host_ed25519_key.pub, comment stripped).
	HostKey string `json:"hostKey,omitempty"`
}
