#!/bin/bash
# neXal mesh sidecar entrypoint. Contract: connector/internal/sandbox/devpod.go
# (SidecarRunArgs / SidecarEnv / awaitJoin) and seed.go (first-boot protocol).
#
# Env (from the 0600 --env-file):
#   NEXAL_HOSTNAME, NEXAL_SETUP_KEY (one-use), NEXAL_LIFECYCLE, NEXAL_MANAGEMENT_URL,
#   optional NEXAL_SSH_CA, NEXAL_DRIVE_MODE, NEXAL_DRIVE_TOKEN.
# Prints on stdout, once joined:   NEXAL-FIRSTBOOT {"meshIp":...}
# or on failure:                   NEXAL-FIRSTBOOT-FAILED <short reason>   (and exits 1)
set -uo pipefail

STATE=/var/lib/nexal
LOG=/var/log/nexal/netbird.log
KEYFILE=/dev/shm/nexal-setup-key
pids=()

fail() {
  local r="${1:-unknown}"
  r=$(printf '%s' "$r" | tr -d '\r\n' | cut -c1-180)
  if [ -s "$LOG" ]; then tail -n 20 "$LOG" >&2; fi
  echo "NEXAL-FIRSTBOOT-FAILED $r"
  exit 1
}
cleanup() {
  shred -u "$KEYFILE" 2>/dev/null || rm -f "$KEYFILE"
  if [ "${#pids[@]}" -gt 0 ]; then kill -TERM "${pids[@]}" 2>/dev/null; wait "${pids[@]}" 2>/dev/null; fi
}
trap cleanup EXIT
trap 'exit 143' TERM INT

[ -n "${NEXAL_SETUP_KEY:-}" ]     || fail "no setup key"
[ -n "${NEXAL_HOSTNAME:-}" ]      || fail "no hostname"
[ -n "${NEXAL_MANAGEMENT_URL:-}" ] || fail "no management url"
[ -c /dev/net/tun ]               || fail "no /dev/net/tun in the sidecar"

mkdir -p "$STATE/netbird" "$STATE/ssh" /var/log/nexal
chmod 0700 "$STATE" "$STATE/netbird" "$STATE/ssh"
export NB_STATE_DIR="$STATE/netbird"
export NB_LAZY_CONN=off   # same as the macOS app: no lazy connections

umask 077
printf '%s' "$NEXAL_SETUP_KEY" > "$KEYFILE"
unset NEXAL_SETUP_KEY

netbird service run --log-file "$LOG" >/dev/null 2>&1 &
pids+=($!)
for _ in $(seq 1 30); do netbird status --check live >/dev/null 2>&1 && break; sleep 1; done
netbird status --check live >/dev/null 2>&1 || fail "mesh daemon did not start"

# --enable-rosenpass selects the ML-KEM-1024 profile; --disable-dns keeps the
# dev container's resolver (shared network namespace) untouched.
timeout 150 netbird up --setup-key-file "$KEYFILE" --management-url "$NEXAL_MANAGEMENT_URL" \
  --enable-rosenpass --disable-dns --hostname "$NEXAL_HOSTNAME" >/dev/null 2>&1 \
  || fail "netbird up failed or timed out"
shred -u "$KEYFILE" 2>/dev/null || rm -f "$KEYFILE"

ip=""
for _ in $(seq 1 60); do
  ip=$(netbird status --json 2>/dev/null | grep -o '"netbirdIp": *"[0-9.]*' | head -n1 | grep -o '[0-9.]*$')
  [ -n "$ip" ] && break
  sleep 1
done
[ -n "$ip" ] || fail "joined but no mesh address"

hostkey_json=""
if [ -n "${NEXAL_SSH_CA:-}" ]; then
  # sshd on the mesh address only; members use CA-signed certificates (principal nexal).
  # NOTE: this shell is inside the SIDECAR container, not the dev container.
  [ -f "$STATE/ssh/ssh_host_ed25519_key" ] || ssh-keygen -q -t ed25519 -N '' -C '' -f "$STATE/ssh/ssh_host_ed25519_key"
  printf '%s\n' "$NEXAL_SSH_CA" > /etc/ssh/nexal_user_ca.pub
  cat > /etc/ssh/nexal_sshd_config <<CFG
ListenAddress $ip
HostKey $STATE/ssh/ssh_host_ed25519_key
TrustedUserCAKeys /etc/ssh/nexal_user_ca.pub
AllowUsers nexal
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
UsePAM no
PidFile none
CFG
  /usr/sbin/sshd -D -e -f /etc/ssh/nexal_sshd_config >/dev/null 2>&1 &
  pids+=($!)
  key=$(cut -d' ' -f1,2 "$STATE/ssh/ssh_host_ed25519_key.pub" 2>/dev/null || ssh-keygen -y -f "$STATE/ssh/ssh_host_ed25519_key" | cut -d' ' -f1,2)
  fp=$(ssh-keygen -lf "$STATE/ssh/ssh_host_ed25519_key.pub" -E sha256 | awk '{print $2}')
  hostkey_json=$(printf ',"hostKeyFingerprint":"%s","hostKey":"%s"' "$fp" "$key")
fi
[ -z "${NEXAL_DRIVE_TOKEN:-}" ] || echo "nexal-sidecar: shared drive is not supported in the sidecar yet; NEXAL_DRIVE_TOKEN ignored" >&2
unset NEXAL_DRIVE_TOKEN

printf 'NEXAL-FIRSTBOOT {"meshIp":"%s"%s}\n' "$ip" "$hostkey_json"

# Supervise: if the mesh daemon (or sshd) dies, exit so Alive() reports it.
wait -n "${pids[@]}"
exit $?
