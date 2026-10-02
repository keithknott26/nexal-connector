#!/bin/bash
# neXal mesh sidecar entrypoint. Contract: connector/internal/sandbox/devpod.go
# (SidecarRunArgs / SidecarEnv / awaitJoin) and seed.go (first-boot protocol).
#
# Input: the 0600 join file $NEXAL_BOOT_FILE (default /run/nexal-boot/mesh.env, a
# per-workspace directory the connector bind-mounts and deletes after the join),
# KEY=VALUE lines, parsed (never sourced):
#   NEXAL_HOSTNAME, NEXAL_SETUP_KEY (one-use), NEXAL_LIFECYCLE,
#   NEXAL_MESH_URL (the VM seed's name; NEXAL_MANAGEMENT_URL is accepted as an alias)
# The same keys are also read from the environment (file wins), for manual tests only:
# a setup key in the environment is visible to `docker inspect`.
# Prints on stdout, once joined:   NEXAL-FIRSTBOOT {"meshIp":...}
# or on failure:                   NEXAL-FIRSTBOOT-FAILED <short reason>   (and exits 1)
# SSH is NOT served here: the connector starts sshd inside the dev container itself
# (it shares this network namespace), see DevSSHDScript in devpod.go.
set -uo pipefail

STATE=/var/lib/nexal
LOG=/var/log/nexal/netbird.log
KEYFILE=/dev/shm/nexal-setup-key
BOOT_FILE=${NEXAL_BOOT_FILE:-/run/nexal-boot/mesh.env}
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

if [ -r "$BOOT_FILE" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    case $line in ''|'#'*) continue ;; esac
    k=${line%%=*}; v=${line#*=}
    case $k in
      NEXAL_HOSTNAME|NEXAL_SETUP_KEY|NEXAL_LIFECYCLE|NEXAL_MESH_URL|NEXAL_MANAGEMENT_URL) printf -v "$k" '%s' "$v" ;;
    esac
  done <"$BOOT_FILE"
  # Best effort: the connector deletes the directory anyway (the mount may be read-only).
  rm -f "$BOOT_FILE" 2>/dev/null || true
fi
MESH_URL=${NEXAL_MESH_URL:-${NEXAL_MANAGEMENT_URL:-}}

[ -n "${NEXAL_SETUP_KEY:-}" ] || fail "no setup key"
[ -n "${NEXAL_HOSTNAME:-}" ]  || fail "no hostname"
[ -n "$MESH_URL" ]            || fail "no management url"
[ -c /dev/net/tun ]           || fail "no /dev/net/tun in the sidecar"

mkdir -p "$STATE/netbird" /var/log/nexal
chmod 0700 "$STATE" "$STATE/netbird"
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
up_out=$(timeout 150 netbird up --setup-key-file "$KEYFILE" --management-url "$MESH_URL" \
  --enable-rosenpass --disable-dns --hostname "$NEXAL_HOSTNAME" 2>&1)
up_rc=$?
if [ $up_rc -ne 0 ]; then
  # Surface netbird's own reason (bad key, management url unreachable, relay/TLS
  # failure, ...) on stderr so it rides along in the sidecar log tail the
  # connector already reads (devpod.go's sidecarLogTail) -- without this, only
  # the generic "failed or timed out" text below ever reaches the user.
  [ -n "$up_out" ] && printf '%s\n' "$up_out" | tail -n 10 >&2
  fail "netbird up failed or timed out"
fi
shred -u "$KEYFILE" 2>/dev/null || rm -f "$KEYFILE"

ip=""
for _ in $(seq 1 60); do
  ip=$(netbird status --json 2>/dev/null | grep -o '"netbirdIp": *"[0-9.]*' | head -n1 | grep -o '[0-9.]*$')
  [ -n "$ip" ] && break
  sleep 1
done
[ -n "$ip" ] || fail "joined but no mesh address"

printf 'NEXAL-FIRSTBOOT {"meshIp":"%s"}\n' "$ip"

# Supervise. A daemon crash (seen: an x/net sendmmsg panic on Linux) must not
# strand the dev container off the network: restart the daemon and rejoin. The
# peer identity stays in $NB_STATE_DIR, so `netbird up` needs no setup key (the
# one-use key is already gone). More than 5 restarts in 10 minutes means
# something is persistently wrong: exit, so the connector's Alive() reports it.
restarts=()
while true; do
  wait -n "${pids[@]}"
  rc=$?
  now=$(date +%s)
  recent=()
  for at in "${restarts[@]}"; do
    [ $((now - at)) -lt 600 ] && recent+=("$at")
  done
  restarts=("${recent[@]}" "$now")
  if [ "${#restarts[@]}" -gt 5 ]; then
    tail -n 20 "$LOG" >&2 2>/dev/null
    echo "NEXAL-MESH-DOWN daemon exited ($rc) more than 5 times in 10 minutes"
    exit "$rc"
  fi
  echo "NEXAL-MESH-RESTART daemon exited ($rc); restart ${#restarts[@]}/5"
  tail -n 5 "$LOG" >&2 2>/dev/null
  sleep 2
  pids=()
  netbird service run --log-file "$LOG" >/dev/null 2>&1 &
  pids+=($!)
  for _ in $(seq 1 30); do netbird status --check live >/dev/null 2>&1 && break; sleep 1; done
  timeout 120 netbird up --management-url "$MESH_URL" --enable-rosenpass --disable-dns \
    --hostname "$NEXAL_HOSTNAME" >/dev/null 2>&1 \
    || echo "NEXAL-MESH-RESTART rejoin did not complete; the daemon keeps retrying"
done
