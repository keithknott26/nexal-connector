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
	// NATMAC and LANMAC, when set, are the guest's two NICs (see GuestMACs): the
	// seed then carries a network-config naming them nat0 (primary route) and
	// lan0 (the Mac's LAN, through the nexal-vmnet bridge), both DHCP.
	NATMAC string
	LANMAC string
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
	if (p.NATMAC != "" || p.LANMAC != "") && (!macPattern.MatchString(p.NATMAC) || !macPattern.MatchString(p.LANMAC) || p.NATMAC == p.LANMAC) {
		return errors.New("invalid guest nic addresses")
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
	renderAppProfile(&b, p)
	renderGuestAgent(&b)
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
	b.WriteString("  - [ sh, -c, \"systemctl daemon-reload; systemctl enable --now nexal-guest-agent.service || true\" ]\n")
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
# Reports go to hvc0 (the virtio console the runner reads; reopened per line,
# since getty's vhangup breaks long-lived descriptors) and /dev/console.
out() { printf '%s\n' "$*" >> /dev/hvc0 2>/dev/null; printf '%s\n' "$*" > /dev/console 2>/dev/null; return 0; }
log() { out "NEXAL-APP $*"; }
# Progress the runner forwards to the apps: "NEXAL-APP-STEP <step> <percent>".
step() { out "NEXAL-APP-STEP $1 $2"; }
ready() { out "NEXAL-APP-READY $NEXAL_APP_PROFILE"; }
if [ -f "$DONE" ]; then
  ready
  exit 0
fi

fail() { out "NEXAL-APP-FAILED $*"; exit 1; }
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

# setup_vnc starts a TigerVNC + XFCE session on the mesh address (port 5900) with the sandbox's VNC
# password from app.env; no display manager runs. Shared by the kali and desktop profiles.
setup_vnc() {
  VNCPW=${NEXAL_VNC_PASSWORD:-}
  systemctl disable --now lightdm.service >/dev/null 2>&1 || true
  systemctl set-default multi-user.target >/dev/null 2>&1 || true
  [ -n "$VNCPW" ] || fail "no VNC password in the seed"
  install -d -o nexal -g nexal -m 700 /home/nexal/.vnc
  printf '%s' "$VNCPW" | vncpasswd -f >/home/nexal/.vnc/passwd || fail "setting the VNC password"
  chown nexal:nexal /home/nexal/.vnc/passwd
  chmod 600 /home/nexal/.vnc/passwd
  sed -i '/^NEXAL_VNC_PASSWORD=/d' /etc/nexal/app.env
  cat >/etc/systemd/system/nexal-vnc.service <<UNIT
[Unit]
Description=neXal remote desktop (TigerVNC)
After=network-online.target nexal-mesh.service
[Service]
User=nexal
Environment=HOME=/home/nexal
ExecStart=/usr/bin/Xtigervnc :0 -rfbport 5900 -interface $IP -rfbauth /home/nexal/.vnc/passwd -SecurityTypes VncAuth -geometry 1600x900 -depth 24 -AlwaysShared
Restart=always
RestartSec=3
[Install]
WantedBy=multi-user.target
UNIT
  cat >/etc/systemd/system/nexal-desktop.service <<UNIT
[Unit]
Description=neXal Kali XFCE session
After=nexal-vnc.service
Requires=nexal-vnc.service
[Service]
User=nexal
Environment=HOME=/home/nexal
Environment=DISPLAY=:0
ExecStartPre=/bin/sleep 2
ExecStart=/usr/bin/dbus-launch --exit-with-session /usr/bin/startxfce4
Restart=always
RestartSec=5
[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now nexal-vnc.service nexal-desktop.service || fail "starting the remote desktop"
  tries=0
  until ss -ltn 2>/dev/null | grep -q "$IP:5900"; do
    tries=$((tries + 1))
    if [ "$tries" -ge 30 ]; then
      fail "the remote desktop did not start on $IP:5900"
    fi
    sleep 2
  done
}

# apt_install runs apt-get install with retries, keeping the output in a file; when it still fails,
# the last lines go to the console so the reason is visible without logging in.
apt_install() {
  APTLOG=/var/log/nexal-apt.log
  mkdir -p /var/log
  retry apt-get install -y -qq "$@" >>"$APTLOG" 2>&1 && return 0
  tail -n 6 "$APTLOG" | while IFS= read -r l; do log "apt: $(printf '%s' "$l" | cut -c1-160)"; done
  df -h / 2>/dev/null | tail -n 1 | while IFS= read -r l; do log "disk: $l"; done
  free -m 2>/dev/null | sed -n 2p | while IFS= read -r l; do log "memory: $l"; done
  return 1
}

# Plain-language progress for the log: "step N/T: what is happening now. Next: what follows."
plan() { log "step $1/$2: $3. Next: $4."; }

# docker pull that stays visibly alive: a heartbeat every 20 s says it is still downloading and how many
# layers are done (a pull prints nothing else to the console). Returns docker's own status.
pull_image() {
  plog=$(mktemp)
  ( s=0; while sleep 20; do s=$((s + 20)); log "still downloading $1 (${s}s, $(grep -c 'Pull complete' "$plog" 2>/dev/null || true) layers done)"; done ) &
  hb=$!
  rc=0
  docker pull "$1" >"$plog" 2>&1 || rc=$?
  kill "$hb" 2>/dev/null || true
  wait "$hb" 2>/dev/null || true
  if [ "$rc" -ne 0 ]; then
    log "download of $1 failed: $(tail -n 1 "$plog" | cut -c1-160)"
  fi
  rm -f "$plog"
  return "$rc"
}

# The version the image reports about itself ("" when it carries none).
image_version() {
  v=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$1" 2>/dev/null || true)
  case "$v" in
  "<no value>") v="" ;;
  esac
  printf '%s' "$v"
}

# Debian 13 splits the docker client into its own package (docker-cli), which docker.io only
# Recommends, and the installs below skip recommends. Ubuntu's docker.io ships the client.
ensure_docker_cli() {
  if ! command -v docker >/dev/null 2>&1; then
    retry apt-get install -y -qq --no-install-recommends docker-cli || fail "installing the docker client"
  fi
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

# The home-network address (lan0, bridged by the Mac's nexal-vmnet helper), if
# any: apps are published there too, so TVs and phones on the LAN reach them.
LANIP=$(ip -4 -o addr show dev lan0 scope global 2>/dev/null | awk '{split($4, a, "/"); print a[1]; exit}' || true)
lan_url() {
  if [ -n "$LANIP" ]; then
    echo " (home network: http://$LANIP:$1)"
  fi
}

case "$NEXAL_APP_PROFILE" in
home-assistant)
  DATA=/var/lib/nexal-homeassistant
  IMAGE=ghcr.io/home-assistant/home-assistant:stable
  log "installing Home Assistant"
  export DEBIAN_FRONTEND=noninteractive
  step app-packages 10
  plan 1 5 "updating the package list" "install Docker"
  retry apt-get update -qq || fail "apt-get update"
  step app-packages 25
  plan 2 5 "installing Docker" "download Home Assistant"
  retry apt-get install -y -qq --no-install-recommends docker.io || fail "installing docker"
  systemctl enable --now docker || fail "starting docker"
  ensure_docker_cli
  step app-download 40
  plan 3 5 "downloading Home Assistant ($IMAGE), usually a few minutes" "start Home Assistant"
  retry pull_image "$IMAGE" || fail "pulling the Home Assistant image"
  VER=$(image_version "$IMAGE")
  log "downloaded Home Assistant${VER:+ v$VER}"
  step app-start 85
  plan 4 5 "starting Home Assistant" "wait for it to be ready"
  mkdir -p "$DATA"
  docker rm -f homeassistant >/dev/null 2>&1 || true
  set -- -p "$IP:8123:8123"
  if [ -n "$LANIP" ]; then
    set -- "$@" -p "$LANIP:8123:8123"
  fi
  docker run -d --name homeassistant --restart unless-stopped "$@" -v "$DATA:/config" -v /etc/localtime:/etc/localtime:ro     "$IMAGE" || fail "starting Home Assistant"
  plan 5 5 "Home Assistant is starting, its first start can take a few minutes" "open http://$IP:8123"
  log "home-assistant ready http://$IP:8123$(lan_url 8123)"
  ;;
jellyfin)
  # Jellyfin media server, published on the mesh address only (port 8096). Media: the
  # shared drive, mounted read-only (rslave, so the drive appears once its mount unit has
  # it), plus the VM's own library folder for files copied in over SFTP.
  # wakeonlan/etherwake are installed so the server can wake a NAS or a sleeping Mac that
  # holds media: run "wakeonlan <mac>" from an SSH session or a Jellyfin plugin or script.
  DATA=/var/lib/nexal-jellyfin
  # GitHub's registry is the first choice (the guest already pulls the mesh runtime from ghcr.io, and
  # Docker Hub rate-limits shared home addresses); Docker Hub is the fallback.
  IMAGE=ghcr.io/jellyfin/jellyfin:latest
  FALLBACK_IMAGE=docker.io/jellyfin/jellyfin:latest
  log "installing Jellyfin"
  export DEBIAN_FRONTEND=noninteractive
  step app-packages 10
  plan 1 5 "updating the package list" "install Docker"
  retry apt-get update -qq || fail "apt-get update"
  step app-packages 25
  plan 2 5 "installing Docker and wake-on-LAN tools" "download Jellyfin"
  retry apt-get install -y -qq --no-install-recommends docker.io wakeonlan etherwake || fail "installing docker"
  systemctl enable --now docker || fail "starting docker"
  ensure_docker_cli
  step app-download 40
  plan 3 5 "downloading Jellyfin ($IMAGE), usually a few minutes" "start Jellyfin"
  if ! retry pull_image "$IMAGE"; then
    log "download from $IMAGE failed, downloading from $FALLBACK_IMAGE instead"
    IMAGE=$FALLBACK_IMAGE
    retry pull_image "$IMAGE" || fail "pulling the Jellyfin image from both registries"
  fi
  VER=$(image_version "$IMAGE")
  log "downloaded Jellyfin${VER:+ v$VER}"
  step app-start 85
  plan 4 5 "starting Jellyfin" "wait for it to answer"
  mkdir -p "$DATA/config" "$DATA/cache" "$DATA/media" /mnt/nexal-drive
  docker rm -f jellyfin >/dev/null 2>&1 || true
  set -- -p "$IP:8096:8096"
  if [ -n "$LANIP" ]; then
    set -- "$@" -p "$LANIP:8096:8096"
  fi
  docker run -d --name jellyfin --restart unless-stopped "$@" \
    -v "$DATA/config:/config" -v "$DATA/cache:/cache" -v "$DATA/media:/media/library" \
    --mount type=bind,source=/mnt/nexal-drive,target=/media/shared-drive,readonly,bind-propagation=rslave \
    -e JELLYFIN_PublishedServerUrl="http://$IP:8096" \
    "$IMAGE" || fail "starting Jellyfin"
  # Ready means answering, not merely started: first start takes a while.
  plan 5 5 "waiting for Jellyfin to answer on http://$IP:8096, up to 5 minutes" "open it and finish the setup wizard"
  n=0
  until curl -fsS -m 5 -o /dev/null "http://$IP:8096/health" 2>/dev/null; do
    n=$((n + 1))
    if [ "$n" -ge 60 ]; then
      log "jellyfin container: $(docker ps -a --filter name=jellyfin --format '{{.Status}}' 2>/dev/null)"
      docker logs --tail 5 jellyfin 2>&1 | while IFS= read -r l; do log "jellyfin: $l"; done
      fail "Jellyfin did not answer on http://$IP:8096 within 5 minutes"
    fi
    sleep 5
  done
  log "jellyfin ready http://$IP:8096$(lan_url 8096)"
  ;;
kali)
  # Kali Linux on the Debian 13 base: the Kali rolling repository is added at low pin priority
  # (Debian stays the default), then the Kali tool set and the XFCE desktop are installed from it.
  # The desktop is a TigerVNC session on the mesh address (port 5900) with the sandbox's VNC
  # password; no display manager runs. It is a large download, so progress is reported as it goes.
  export DEBIAN_FRONTEND=noninteractive
  KALI_KEYRING=/usr/share/keyrings/kali-archive-keyring.gpg
  # The desktop flag and VNC password come from app.env (first-boot.env is gone by now).
  VNCPW=${NEXAL_VNC_PASSWORD:-}
  WANT_DESKTOP=${NEXAL_DESKTOP:-0}
  log "installing Kali Linux"
  step app-packages 5
  plan 1 5 "updating the package list" "add the Kali package source"
  retry apt-get update -qq || fail "apt-get update"
  retry apt-get install -y -qq --no-install-recommends ca-certificates curl gnupg || fail "installing curl and gnupg"
  step app-packages 12
  plan 2 5 "adding the Kali package source" "install the Kali tools and desktop"
  retry curl -fsSL -o /tmp/kali-archive-key.asc https://archive.kali.org/archive-key.asc || fail "downloading the Kali archive key"
  gpg --dearmor </tmp/kali-archive-key.asc >"$KALI_KEYRING" || fail "reading the Kali archive key"
  rm -f /tmp/kali-archive-key.asc
  chmod 644 "$KALI_KEYRING"
  printf 'deb [signed-by=%s] http://http.kali.org/kali kali-rolling main contrib non-free non-free-firmware\n' "$KALI_KEYRING" >/etc/apt/sources.list.d/kali.list
  # Kali's tools need newer builds of many shared libraries than Debian 13 ships, so mixing the two
  # repositories cannot resolve (tried: low pin, -t kali-rolling). Kali becomes the only source, as in
  # Kali's own Debian migration; Debian's list is kept aside in /root/apt-debian.
  mkdir -p /root/apt-debian
  for f in /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list.d/debian.list; do
    if [ -e "$f" ]; then mv "$f" /root/apt-debian/; fi
  done
  rm -f /etc/apt/preferences.d/kali
  retry apt-get update -qq || fail "apt-get update (Kali)"
  step app-download 25
  plan 3 5 "installing Kali Linux and its tools, a large download (several GB), usually 10 to 30 minutes" "set up the remote desktop"
  # Progress while apt downloads: bytes already in the package cache against the size apt announced.
  rm -f /run/nexal-apt-total
  (
    s=0
    while sleep 15; do
      s=$((s + 15))
      total=$(cat /run/nexal-apt-total 2>/dev/null || echo 0)
      have=$(find /var/cache/apt/archives -maxdepth 1 -name '*.deb' -printf '%s\n' 2>/dev/null | awk '{t += $1} END {print t + 0}')
      if [ "$total" -gt 0 ]; then
        pct=$((25 + have * 50 / total)); [ "$pct" -gt 75 ] && pct=75
        step app-download "$pct"
        if [ $((s % 60)) -eq 0 ]; then log "downloaded $((have / 1048576)) of $((total / 1048576)) MB"; fi
      elif [ $((s % 60)) -eq 0 ]; then
        log "still installing Kali ($((s / 60)) min)"
      fi
    done
  ) &
  hb=$!
  rc=0
  if [ "$WANT_DESKTOP" = "1" ]; then
    KALI_PKGS="kali-linux-default kali-desktop-xfce xorg dbus-x11 tigervnc-standalone-server tigervnc-tools"
  else
    KALI_PKGS="kali-linux-headless"
  fi
  # Debian's security updates (libexpat1 first) are newer than Kali's builds: apt must be allowed to
  # downgrade them, and the libexpat1 downgrade has to be requested by name before the tool set.
  # Nothing may start while packages install (apache2 and others would grab ports and fail), and the
  # debconf questions (kismet, atftpd) get their defaults instead of a text prompt.
  # apt-listchanges (a Python hook) crashes on this guest and fails every install; it only shows news.
  rm -f /etc/apt/apt.conf.d/20listchanges
  printf '#!/bin/sh\nexit 101\n' >/usr/sbin/policy-rc.d
  chmod 755 /usr/sbin/policy-rc.d
  printf 'atftpd atftpd/use_inetd boolean false\n' | debconf-set-selections 2>/dev/null || true
  # Plain "libexpat1" is "already the newest version" to apt, so the downgrade names Kali's exact version.
  EXPAT=$(apt-cache madison libexpat1 2>/dev/null | awk '/kali/ {print $3; exit}')
  if [ -n "$EXPAT" ]; then
    apt_install --allow-downgrades "libexpat1=$EXPAT" || rc=$?
  fi
  if [ "$rc" -eq 0 ]; then
    # shellcheck disable=SC2086
    apt-get install -s -y --no-install-recommends --allow-downgrades $KALI_PKGS 2>/dev/null \
      | sed -n 's/^Need to get \([0-9,.]*\) \([kMG]\)B.*/\1 \2/p' | head -n 1 \
      | awk '{gsub(",", "", $1); m = ($2 == "G") ? 1073741824 : ($2 == "M") ? 1048576 : 1024; printf "%d\n", $1 * m}' >/run/nexal-apt-total 2>/dev/null || true
    # shellcheck disable=SC2086
    apt_install --no-install-recommends --allow-downgrades -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold $KALI_PKGS || rc=$?
  fi
  rm -f /usr/sbin/policy-rc.d
  kill "$hb" 2>/dev/null || true
  wait "$hb" 2>/dev/null || true
  if [ "$rc" -ne 0 ]; then
    fail "installing Kali packages"
  fi
  # Tool packages bring web, TFTP and database servers with them; leave them stopped and disabled
  # (start one when needed) instead of listening on the mesh address.
  systemctl disable --now apache2.service apache-htcacheclean.service atftpd.service nginx.service >/dev/null 2>&1 || true
  if [ "$WANT_DESKTOP" = "1" ]; then
    step app-start 80
    plan 4 5 "setting up the remote desktop" "check it answers"
    setup_vnc
  fi
  step app-start 95
  plan 5 5 "Kali Linux is ready" "connect with Desktop, or SSH as nexal"
  log "kali ready desktop vnc://$IP:5900 (user nexal; the VNC password is shown in the app)"
  ;;
desktop)
  # A plain Ubuntu/Debian VM that asked for a desktop: XFCE over TigerVNC on the mesh address.
  export DEBIAN_FRONTEND=noninteractive
  log "installing the remote desktop"
  step app-packages 10
  plan 1 3 "updating the package list" "install the desktop"
  retry apt-get update -qq || fail "apt-get update"
  step app-download 30
  plan 2 3 "installing the XFCE desktop and VNC server, usually a few minutes" "start the remote desktop"
  # shellcheck disable=SC2086
  apt_install --no-install-recommends xfce4 xfce4-terminal xorg dbus-x11 tigervnc-standalone-server tigervnc-tools || fail "installing the desktop packages"
  step app-start 80
  plan 3 3 "starting the remote desktop" "connect with Desktop"
  setup_vnc
  step app-start 95
  log "desktop ready vnc://$IP:5900 (user nexal; the VNC password is shown in the app)"
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
func renderAppProfile(b *strings.Builder, p SeedParams) {
	profile := p.AppProfile
	if profile == "" || profile == AppProfileNone {
		return
	}
	// The first-boot script deletes first-boot.env once it has joined the mesh, so a profile that
	// needs the desktop flag or the VNC password gets its own copy here (root-only, and the
	// profile removes the password line after use).
	perms := "0644"
	extra := ""
	if profile == AppProfileKali || profile == AppProfileDesktop {
		perms = "0600"
		extra = "      NEXAL_DESKTOP=" + boolDigit(p.Desktop) + "\n      NEXAL_VNC_PASSWORD=" + p.VNCPassword + "\n"
	}
	b.WriteString("  - path: /etc/nexal/app.env\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"" + perms + "\"\n")
	b.WriteString("    content: |\n")
	b.WriteString("      NEXAL_APP_PROFILE=" + profile + "\n")
	b.WriteString(extra)
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
	if p.LANMAC != "" {
		if err := os.WriteFile(filepath.Join(dir, "network-config"), []byte(RenderNetworkConfig(p)), 0o600); err != nil {
			return err
		}
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
	// LanIP is the guest's address on the Mac's home network (the bridged lan0
	// NIC), when it has one.
	LanIP string `json:"lanIp,omitempty"`
}
