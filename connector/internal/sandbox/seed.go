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
	"regexp"
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
	// AppProfile installs one application on first boot ("" or "none" for a plain
	// image); see renderAppProfile.
	AppProfile string
	// MeshImage is the OCI image (the dev-container mesh sidecar) that carries the
	// patched NetBird runtime every peer must run. Stock cloud images have no
	// neXal runtime, so seedFirstBootScript pulls the netbird binary out of this
	// image's linux layer on first boot. Not secret; rendered into its own file.
	MeshImage string
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
	if !KnownAppProfile(p.AppProfile) {
		return errors.New("unknown app profile")
	}
	if p.MeshImage != "" && !imageRefPattern.MatchString(p.MeshImage) {
		return errors.New("invalid mesh runtime image")
	}
	return nil
}

// imageRefPattern accepts registry/repo[/...]:tag (no digest form, no scheme):
// the value is interpolated into a guest file and handed to the fetcher.
var imageRefPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?(/[a-z0-9]+([._-][a-z0-9]+)*)+:[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

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
	if ap := p.AppProfile; ap != "" && ap != AppProfileNone {
		w("      NEXAL_APP_PROFILE=%s\n", ap)
	}
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
	renderMeshRuntime(&b, p.MeshImage)
	renderAppProfile(&b, p.AppProfile)
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
	// A pre-baked neXal-ready image ships /usr/local/sbin/nexal-first-boot; the
	// stock cloud images the catalog actually serves do not, so the seed brings its
	// own (seedFirstBootScript). Either way the report must land on hvc0, the
	// virtio console the runner logs: on stock images the kernel console (and so
	// /dev/console) is tty0/ttyAMA0, which nobody reads -- writing only there made
	// every VM time out silently, even its "no first-boot script" failure line.
	// hvc0 is reopened for every line: when serial-getty starts on hvc0 it calls
	// vhangup(), which permanently breaks any descriptor opened on it earlier (a
	// long-lived `tee /dev/hvc0` lost everything printed after the login prompt,
	// including the NEXAL-FIRSTBOOT line of a VM that had joined).
	b.WriteString("  - [ sh, -c, \"if [ -x /usr/local/sbin/nexal-first-boot ]; then S=/usr/local/sbin/nexal-first-boot; else S=" + seedFirstBootPath + "; fi; $S 2>&1 | while IFS= read -r l; do printf '%s\\\\n' \\\"$l\\\" >> /dev/hvc0; printf '%s\\\\n' \\\"$l\\\" >> /dev/console; done 2>/dev/null; true\" ]\n")
	if p.AppProfile != "" && p.AppProfile != AppProfileNone {
		// Started after the first-boot script so the mesh address already exists, and
		// --no-block so a slow install never holds up cloud-init (or the boot).
		b.WriteString("  - [ systemctl, enable, nexal-app.service ]\n")
		b.WriteString("  - [ systemctl, start, --no-block, nexal-app.service ]\n")
	}
	return b.String()
}

// appSetupScript is the first-boot installer for an app profile. It is POSIX sh,
// idempotent (a done-file short-circuits a reset or reboot), retries the network
// steps, and binds the application to the mesh address only - the same rule the
// first-boot script applies to sshd and VNC. Progress goes to the console, which
// the runner already reads.
const appSetupScript = `#!/bin/sh
set -eu
STATE_DIR=/var/lib/nexal
. /etc/nexal/app.env
DONE="$STATE_DIR/app-$NEXAL_APP_PROFILE.done"
log() { echo "NEXAL-APP $*" > /dev/console 2>/dev/null || true; }
# Progress the runner forwards to the apps: "NEXAL-APP-STEP <step> <percent>".
step() { echo "NEXAL-APP-STEP $1 $2" > /dev/console 2>/dev/null || true; }
ready() { echo "NEXAL-APP-READY $NEXAL_APP_PROFILE" > /dev/console 2>/dev/null || true; }
if [ -f "$DONE" ]; then
  ready
  exit 0
fi

fail() { echo "NEXAL-APP-FAILED $*" > /dev/console 2>/dev/null || true; exit 1; }
# Every "[ x ] && y" here is written as an if: under set -e a false test is a
# failed command and would end the script.
retry() {
  n=0
  until "$@"; do
    n=$((n + 1))
    if [ "$n" -ge 5 ]; then
      return 1
    fi
    sleep 10
  done
  return 0
}

# The mesh address (CGNAT 100.64.0.0/10) is the only address the app is published on.
mesh_ip() {
  ip -4 -o addr show 2>/dev/null | awk '{print $4}' | cut -d/ -f1 |
    awk -F. '$1 == 100 && $2 >= 64 && $2 <= 127 { print; exit }'
}
IP=""
tries=0
while [ -z "$IP" ]; do
  IP=$(mesh_ip || true)
  if [ -n "$IP" ]; then
    break
  fi
  tries=$((tries + 1))
  if [ "$tries" -ge 60 ]; then
    fail "no mesh address after 5 minutes"
  fi
  sleep 5
done

case "$NEXAL_APP_PROFILE" in
home-assistant)
  DATA=/var/lib/nexal-homeassistant
  IMAGE=ghcr.io/home-assistant/home-assistant:stable
  log "installing Home Assistant"
  export DEBIAN_FRONTEND=noninteractive
  step app-packages 10
  retry apt-get update -qq || fail "apt-get update"
  step app-packages 25
  retry apt-get install -y -qq --no-install-recommends docker.io || fail "installing docker"
  systemctl enable --now docker || fail "starting docker"
  step app-download 40
  retry docker pull "$IMAGE" || fail "pulling the Home Assistant image"
  step app-start 85
  mkdir -p "$DATA"
  docker rm -f homeassistant >/dev/null 2>&1 || true
  docker run -d --name homeassistant --restart unless-stopped     -p "$IP:8123:8123" -v "$DATA:/config" -v /etc/localtime:/etc/localtime:ro     "$IMAGE" || fail "starting Home Assistant"
  log "home-assistant ready http://$IP:8123"
  ;;
jellyfin)
  # Jellyfin media server, published on the mesh address only (port 8096). Media: the
  # shared drive, mounted read-only (rslave, so the drive appears once its mount unit has
  # it), plus the VM's own library folder for files copied in over SFTP.
  # wakeonlan/etherwake are installed so the server can wake a NAS or a sleeping Mac that
  # holds media: run "wakeonlan <mac>" from an SSH session or a Jellyfin plugin or script.
  DATA=/var/lib/nexal-jellyfin
  IMAGE=docker.io/jellyfin/jellyfin:latest
  log "installing Jellyfin"
  export DEBIAN_FRONTEND=noninteractive
  step app-packages 10
  retry apt-get update -qq || fail "apt-get update"
  step app-packages 25
  retry apt-get install -y -qq --no-install-recommends docker.io wakeonlan etherwake || fail "installing docker"
  systemctl enable --now docker || fail "starting docker"
  step app-download 40
  retry docker pull "$IMAGE" || fail "pulling the Jellyfin image"
  step app-start 85
  mkdir -p "$DATA/config" "$DATA/cache" "$DATA/media" /mnt/nexal-drive
  docker rm -f jellyfin >/dev/null 2>&1 || true
  docker run -d --name jellyfin --restart unless-stopped \
    -p "$IP:8096:8096" \
    -v "$DATA/config:/config" -v "$DATA/cache:/cache" -v "$DATA/media:/media/library" \
    --mount type=bind,source=/mnt/nexal-drive,target=/media/shared-drive,readonly,bind-propagation=rslave \
    -e JELLYFIN_PublishedServerUrl="http://$IP:8096" \
    "$IMAGE" || fail "starting Jellyfin"
  log "jellyfin ready http://$IP:8096"
  ;;
*)
  fail "unknown app profile"
  ;;
esac

mkdir -p "$STATE_DIR"
: > "$DONE"
ready
`

// appUnit runs appSetupScript once per sandbox. docker.service does not exist until
// the script installs it; an After= on a missing unit is simply ignored.
const appUnit = `[Unit]
Description=neXal app setup
Wants=network-online.target
After=network-online.target nexal-mesh.service docker.service
[Service]
Type=oneshot
RemainAfterExit=yes
EnvironmentFile=/etc/nexal/app.env
ExecStart=/usr/local/sbin/nexal-app-setup
TimeoutStartSec=1800
[Install]
WantedBy=multi-user.target
`

// writeIndented writes a cloud-init block scalar body: every line of text at six
// spaces, blank lines left empty so the block is not closed early.
func writeIndented(b *strings.Builder, text string) {
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString("      " + line + "\n")
	}
}

// renderAppProfile adds the app-profile env file, installer and unit to write_files.
// It renders nothing for a plain image.
func renderAppProfile(b *strings.Builder, profile string) {
	if profile == "" || profile == AppProfileNone {
		return
	}
	b.WriteString("  - path: /etc/nexal/app.env\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0644\"\n")
	b.WriteString("    content: |\n")
	b.WriteString("      NEXAL_APP_PROFILE=" + profile + "\n")
	b.WriteString("  - path: /usr/local/sbin/nexal-app-setup\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0755\"\n")
	b.WriteString("    content: |\n")
	writeIndented(b, appSetupScript)
	b.WriteString("  - path: /etc/systemd/system/nexal-app.service\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0644\"\n")
	b.WriteString("    content: |\n")
	writeIndented(b, appUnit)
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
