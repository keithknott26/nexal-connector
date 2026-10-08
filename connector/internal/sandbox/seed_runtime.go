package sandbox

import "strings"

// Stock-image first boot.
//
// The catalog serves vendor cloud images (cloud.debian.org genericcloud,
// cloud-images.ubuntu.com), which carry neither the neXal first-boot script nor
// the patched NetBird runtime every peer must run (a stock NetBird is refused by
// the ML-KEM gate). The seed therefore brings both pieces itself:
//
//   - fetchRuntimePy pulls the netbird binary out of the mesh sidecar image's
//     linux/<arch> layer straight from the registry (anonymous, digest-checked).
//     Every cloud image has python3: cloud-init itself is python.
//   - seedFirstBootScript installs it as nexal-mesh.service, joins with the
//     one-use setup key exactly the way the dev-container sidecar does
//     (connector/sidecar/entrypoint.sh), and prints the NEXAL-FIRSTBOOT line.
//
// runcmd pipes the script's output to /dev/hvc0, the console the runner reads.
const (
	seedFirstBootPath   = "/usr/local/sbin/nexal-seed-first-boot"
	fetchRuntimePath    = "/usr/local/lib/nexal/fetch-runtime.py"
	meshRuntimeEnvPath  = "/etc/nexal/mesh-runtime.env"
	meshRuntimeBinMatch = "nexal-mlkem1024"
)

// renderMeshRuntime adds the runtime image reference, the fetcher and the
// stock-image first-boot script to write_files.
func renderMeshRuntime(b *strings.Builder, image string) {
	b.WriteString("  - path: " + meshRuntimeEnvPath + "\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0644\"\n")
	b.WriteString("    content: |\n")
	b.WriteString("      NEXAL_MESH_IMAGE=" + image + "\n")
	b.WriteString("  - path: " + fetchRuntimePath + "\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0755\"\n")
	b.WriteString("    content: |\n")
	writeIndented(b, fetchRuntimePy)
	b.WriteString("  - path: " + seedFirstBootPath + "\n")
	b.WriteString("    owner: root:root\n")
	b.WriteString("    permissions: \"0755\"\n")
	b.WriteString("    content: |\n")
	writeIndented(b, seedFirstBootScript)
}

// fetchRuntimePy extracts one file from a public OCI image without docker:
//
//	fetch-runtime.py <registry/repo:tag> <path/in/image> <dest>
//
// It resolves the multi-arch index to linux/<this arch>, downloads layers newest
// first, checks each blob's sha256 against its digest, and stops at the first
// layer that contains the path.
const fetchRuntimePy = `#!/usr/bin/env python3
import hashlib, json, os, platform, re, sys, tarfile, tempfile, time, urllib.error, urllib.request

ACCEPT = ", ".join([
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
])

def die(msg):
    print("fetch-runtime: " + msg, file=sys.stderr)
    sys.exit(1)

def main():
    if len(sys.argv) != 4:
        die("usage: fetch-runtime.py <registry/repo:tag> <path> <dest>")
    ref, member, dest = sys.argv[1], sys.argv[2].lstrip("./"), sys.argv[3]
    registry, rest = ref.split("/", 1)
    repo, tag = rest.rsplit(":", 1)
    arch = {"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64", "amd64": "amd64"}.get(platform.machine())
    if not arch:
        die("unsupported architecture " + platform.machine())
    # Plain http only for a registry on this machine (a local mirror or a test).
    scheme = "http" if registry.split(":")[0] in ("localhost", "127.0.0.1") else "https"
    base = scheme + "://" + registry + "/v2/" + repo
    token = [None]

    def open_url(url, accept=None):
        last = None
        for attempt in range(6):
            req = urllib.request.Request(url)
            if accept:
                req.add_header("Accept", accept)
            if token[0]:
                # Unredirected: blob GETs 307 to a signed CDN URL, which must not
                # receive (and may reject) the registry token.
                req.add_unredirected_header("Authorization", "Bearer " + token[0])
            try:
                return urllib.request.urlopen(req, timeout=60)
            except urllib.error.HTTPError as e:
                if e.code == 401 and not token[0]:
                    token[0] = get_token(e.headers.get("WWW-Authenticate", ""))
                    continue
                last = "HTTP %d for %s" % (e.code, url.split("?")[0])
                if e.code < 500 and e.code != 429:
                    break
            except Exception as e:
                last = "%s for %s" % (e, url.split("?")[0])
            time.sleep(min(5 * (attempt + 1), 20))
        die(last or "request failed")

    def get_token(challenge):
        fields = dict(re.findall(r'(\w+)="([^"]*)"', challenge))
        realm = fields.get("realm")
        if not realm:
            die("registry asked for auth without a bearer realm")
        q = "?scope=repository:%s:pull" % repo
        if fields.get("service"):
            q += "&service=" + fields["service"]
        with urllib.request.urlopen(realm + q, timeout=60) as r:
            body = json.load(r)
        return body.get("token") or body.get("access_token")

    def manifest(ref_or_digest):
        with open_url(base + "/manifests/" + ref_or_digest, ACCEPT) as r:
            return json.load(r)

    m = manifest(tag)
    if "manifests" in m:
        pick = [d for d in m["manifests"]
                if d.get("platform", {}).get("os") == "linux" and d.get("platform", {}).get("architecture") == arch]
        if not pick:
            die("image has no linux/" + arch + " variant")
        m = manifest(pick[0]["digest"])
    layers = m.get("layers") or []
    if not layers:
        die("image manifest lists no layers")

    for layer in reversed(layers):
        digest = layer["digest"]
        algo, want = digest.split(":", 1)
        h = hashlib.new(algo)
        with tempfile.TemporaryFile() as blob:
            with open_url(base + "/blobs/" + digest) as r:
                while True:
                    chunk = r.read(1 << 20)
                    if not chunk:
                        break
                    h.update(chunk)
                    blob.write(chunk)
            if h.hexdigest() != want:
                die("layer digest mismatch for " + digest)
            blob.seek(0)
            with tarfile.open(fileobj=blob, mode="r:*") as tf:
                for ti in tf:
                    if ti.name.lstrip("./") == member and ti.isreg():
                        src = tf.extractfile(ti)
                        os.makedirs(os.path.dirname(dest) or ".", exist_ok=True)
                        tmp = dest + ".part"
                        with open(tmp, "wb") as out:
                            while True:
                                chunk = src.read(1 << 20)
                                if not chunk:
                                    break
                                out.write(chunk)
                        os.chmod(tmp, 0o755)
                        os.replace(tmp, dest)
                        print("fetch-runtime: installed %s from %s (%s)" % (dest, ref, digest[:19]))
                        return
    die(member + " is not in " + ref)

main()
`

// seedFirstBootScript joins a stock cloud image to the mesh. Its contract is the
// one ParseFirstBootLine reads: exactly one NEXAL-FIRSTBOOT {...} or
// NEXAL-FIRSTBOOT-FAILED <reason> line on stdout. Progress lines go to stdout
// too, so the console tail in a timeout error says how far it got.
const seedFirstBootScript = `#!/bin/sh
# neXal first boot for stock cloud images (installed by the connector's seed).
set -u
umask 077
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
ENV_FILE=/etc/nexal/first-boot.env
RUNTIME_ENV=/etc/nexal/mesh-runtime.env
BIN=/usr/local/bin/netbird
LOG=/var/log/nexal/netbird.log
KEYFILE=/run/nexal-setup-key
export NB_STATE_DIR=/var/lib/nexal/netbird NB_LAZY_CONN=off NB_WG_KERNEL_DISABLED=true

say() { echo "nexal-first-boot: $*"; }
scrub() {
  for f in "$KEYFILE" "$ENV_FILE" /var/lib/cloud/instance/user-data.txt* /var/lib/cloud/instance/cloud-config.txt \
           /var/lib/cloud/instances/*/user-data.txt* /var/lib/cloud/instances/*/cloud-config.txt; do
    [ -e "$f" ] && { shred -u "$f" 2>/dev/null || rm -f "$f"; }
  done
  return 0
}
fail() {
  r=$(printf '%s' "$*" | tr -d '\r\n' | cut -c1-180)
  if [ -s "$LOG" ]; then
    tail -n 200 "$LOG" | grep -Ei 'warn|error|fail|denied|refused|timeout|unreachable|invalid|x509|no such host' | tail -n 5
  fi
  echo "NEXAL-FIRSTBOOT-FAILED $r"
  scrub
  exit 1
}

NEXAL_SETUP_KEY= NEXAL_HOSTNAME= NEXAL_MESH_URL= NEXAL_MESH_IMAGE=
for f in "$ENV_FILE" "$RUNTIME_ENV"; do
  [ -r "$f" ] || continue
  while IFS= read -r line || [ -n "$line" ]; do
    case $line in ''|'#'*) continue ;; esac
    k=${line%%=*}; v=${line#*=}
    case $k in
      NEXAL_SETUP_KEY) NEXAL_SETUP_KEY=$v ;;
      NEXAL_HOSTNAME) NEXAL_HOSTNAME=$v ;;
      NEXAL_MESH_URL) NEXAL_MESH_URL=$v ;;
      NEXAL_MESH_IMAGE) NEXAL_MESH_IMAGE=$v ;;
    esac
  done <"$f"
done
[ -n "$NEXAL_SETUP_KEY" ] || fail "no setup key in the seed"
[ -n "$NEXAL_HOSTNAME" ]  || fail "no hostname in the seed"
[ -n "$NEXAL_MESH_URL" ]  || fail "no management url in the seed"
mkdir -p /var/log/nexal "$NB_STATE_DIR"
chmod 0700 /var/lib/nexal "$NB_STATE_DIR"

say "waiting for the network"
i=0
while ! ip route 2>/dev/null | grep -q '^default'; do
  i=$((i + 1))
  [ $i -ge 120 ] && fail "the VM got no network address (no DHCP lease from the Mac's NAT; is macOS bootpd running?)"
  sleep 1
done
MGMT_HOST=$(printf '%s' "$NEXAL_MESH_URL" | sed -E 's#^[a-z]+://##; s#[/:].*##')
i=0
until getent hosts "$MGMT_HOST" >/dev/null 2>&1; do
  i=$((i + 1))
  [ $i -ge 60 ] && fail "cannot resolve $MGMT_HOST from the VM (DNS)"
  sleep 1
done

if ! "$BIN" version 2>/dev/null | grep -q '` + meshRuntimeBinMatch + `'; then
  [ -n "$NEXAL_MESH_IMAGE" ] || fail "no neXal mesh runtime in the image and no runtime image in the seed"
  say "downloading the mesh runtime from $NEXAL_MESH_IMAGE"
  out=$(python3 ` + fetchRuntimePath + ` "$NEXAL_MESH_IMAGE" usr/local/bin/netbird "$BIN" 2>&1) || fail "mesh runtime download failed: $(printf '%s' "$out" | tail -n 1)"
  "$BIN" version 2>/dev/null | grep -q '` + meshRuntimeBinMatch + `' || fail "downloaded mesh runtime is not the neXal build"
fi
say "mesh runtime $("$BIN" version 2>/dev/null)"

modprobe tun 2>/dev/null || true
[ -c /dev/net/tun ] || { mkdir -p /dev/net; mknod /dev/net/tun c 10 200 2>/dev/null; }
[ -c /dev/net/tun ] || fail "no /dev/net/tun in the VM"

cat >/etc/systemd/system/nexal-mesh.service <<UNIT
[Unit]
Description=neXal mesh runtime
After=network-online.target
Wants=network-online.target
[Service]
Environment=NB_STATE_DIR=$NB_STATE_DIR NB_LAZY_CONN=off NB_WG_KERNEL_DISABLED=true
ExecStart=$BIN service run --log-file $LOG
Restart=always
RestartSec=3
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now nexal-mesh.service >/dev/null 2>&1 || fail "could not start nexal-mesh.service"
i=0
until "$BIN" status --check live >/dev/null 2>&1; do
  i=$((i + 1))
  [ $i -ge 90 ] && fail "mesh daemon did not start within 90s"
  sleep 1
done

# This peer's address once management AND signal are connected (signal only comes
# up once the engine runs), else nothing.
connected_ip() {
  "$BIN" status --json 2>/dev/null | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(0)
if d.get("management", {}).get("connected") and d.get("signal", {}).get("connected"):
    print((d.get("netbirdIp") or "").split("/")[0])
'
}

say "joining the mesh as $NEXAL_HOSTNAME"
printf '%s' "$NEXAL_SETUP_KEY" >"$KEYFILE"
NEXAL_SETUP_KEY=
up_out=$(timeout 300 "$BIN" up --setup-key-file "$KEYFILE" --management-url "$NEXAL_MESH_URL" \
  --enable-rosenpass --disable-dns --hostname "$NEXAL_HOSTNAME" 2>&1)
up_rc=$?
shred -u "$KEYFILE" 2>/dev/null || rm -f "$KEYFILE"
# netbird up gives up after the daemon's own 50s wait while the daemon keeps
# joining (the ML-KEM handshake is slow on a busy Mac); wait for the real result.
ip=""
i=0
while [ $i -lt 100 ]; do
  ip=$(connected_ip)
  [ -n "$ip" ] && break
  systemctl is-active --quiet nexal-mesh.service || break
  i=$((i + 1))
  sleep 3
done
if [ -z "$ip" ]; then
  reason=$(printf '%s\n' "$up_out" | grep -v '^[[:space:]]*$' | tail -n 2 | tr '\n' ' ' | cut -c1-200)
  fail "mesh join failed (netbird up exit $up_rc): ${reason:-no output}"
fi

[ -e /etc/ssh/ssh_host_ed25519_key ] || ssh-keygen -q -t ed25519 -N '' -f /etc/ssh/ssh_host_ed25519_key
systemctl restart ssh 2>/dev/null || systemctl restart sshd 2>/dev/null || true
fp=$(ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub -E sha256 2>/dev/null | awk '{print $2}')
key=$(awk 'NR==1{print $1" "$2}' /etc/ssh/ssh_host_ed25519_key.pub 2>/dev/null)
scrub
if [ -n "$fp" ] && [ -n "$key" ]; then
  printf 'NEXAL-FIRSTBOOT {"meshIp":"%s","hostKeyFingerprint":"%s","hostKey":"%s"}\n' "$ip" "$fp" "$key"
else
  printf 'NEXAL-FIRSTBOOT {"meshIp":"%s"}\n' "$ip"
fi
`
