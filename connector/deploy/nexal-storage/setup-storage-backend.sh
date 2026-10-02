#!/usr/bin/env bash
# neXal storage: choose the dev-container workspace storage backend (run as root).
# Run this as part of step 3 in README.md, after useradd/usermod but before step 5's
# enrollment -- it creates /var/lib/nexal/devcontainers either way.
#
#   local: workspace source trees live on this box's own disk (the original design;
#          needs real free local space, capped by NEXAL_MANAGED_STORAGE_OPT / xfs pquota).
#   s3:    workspace source trees live on S3-compatible object storage via JuiceFS (own
#          bucket, your keys) -- lots of cheap capacity instead of local disk, so an
#          operator with a small or network-storage-less box can still run neXal
#          storage with the SAME managed-host capabilities as a local-disk one.
#
# Only /var/lib/nexal/devcontainers moves. Docker's own image/container storage
# (/var/lib/docker) always stays on local disk -- that is the container runtime's hot
# path, and FUSE/object-storage latency there would make every container slow to boot.
# Workspace source trees (git checkouts, project files) are exactly what JuiceFS is
# good at: this is the same pattern already proven for Time Machine storage, see
# nexal-platform's self-hosted-config/gateway/bootstrap.sh.
#
#   sudo ./setup-storage-backend.sh local
#   sudo ./setup-storage-backend.sh s3        # prompts for bucket + keys, never on argv
#
# Quota note: NEXAL_MANAGED_STORAGE_OPT (per-tenant disk quota) only works on the local
# backend (overlay2 on xfs with pquota). The s3 backend has no per-tenant quota wired up
# yet -- same caveat the Time Machine gateway already carries ("replace with `juicefs
# quota` stats before hundreds of tenants"); fine for a handful of members, not yet for
# a public offering.
set -euo pipefail
umask 077

STATE=/var/lib/nexal
WORKSPACES="$STATE/devcontainers"
ETC=/etc/nexal
CACHE=/var/cache/juicefs/nexal-storage
META="sqlite3://$STATE/jfs.db"
VOLUME=nexal-storage
HERE="$(cd "$(dirname "$0")" && pwd)"

die() { echo "error: $*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || die "run as root"
ask() { local var=$1 prompt=$2 def=${3:-}; local v; read -r -p "$prompt${def:+ [$def]}: " v; printf -v "$var" '%s' "${v:-$def}"; }
secret() { local var=$1 prompt=$2; local v; read -r -s -p "$prompt: " v; echo; [ -n "$v" ] || die "$prompt is required"; printf -v "$var" '%s' "$v"; }
own() { chown nexal:nexal "$1" 2>/dev/null || true; }   # nexal user may not exist yet on a fresh box

install -d -m 0700 "$STATE"; own "$STATE"

case "${1:-}" in
  local)
    [ -d "$WORKSPACES" ] && die "$WORKSPACES already exists; remove it first if you really want to switch backends (this does not migrate data)"
    install -d -m 0700 "$WORKSPACES"; own "$WORKSPACES"
    echo "Workspaces on local disk: $WORKSPACES"
    ;;
  s3)
    [ -d "$WORKSPACES" ] && die "$WORKSPACES already exists; remove it first if you really want to switch backends (this does not migrate data)"
    ask BUCKET "Object storage bucket URL (e.g. an R2 bucket's S3 endpoint, or any S3-compatible endpoint)" ""
    [[ "$BUCKET" =~ ^https:// ]] || die "bucket URL must be https"
    secret ACCESS_KEY "S3 access key"
    secret SECRET_KEY "S3 secret key"
    ask CACHE_GB "Local JuiceFS cache size in GB" "20"

    echo "== packages"
    command -v fusermount3 >/dev/null 2>&1 || command -v fusermount >/dev/null 2>&1 || {
      apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq fuse3 >/dev/null
    }
    grep -q '^user_allow_other' /etc/fuse.conf 2>/dev/null || echo user_allow_other >> /etc/fuse.conf

    echo "== JuiceFS"
    command -v juicefs >/dev/null || curl -fsSL https://d.juicefs.com/install | sh -
    install -d -m 0711 "$CACHE"
    install -d -m 0700 "$ETC"
    # Client-side encryption: the object storage provider only ever holds ciphertext.
    [ -f "$ETC/jfs-rsa.pem" ] || openssl genrsa -out "$ETC/jfs-rsa.pem" 3072 2>/dev/null
    chmod 0600 "$ETC/jfs-rsa.pem"
    if ! juicefs status "$META" >/dev/null 2>&1; then
      # Secret key is passed via env so it is not visible in the process list.
      ACCESS_KEY="$ACCESS_KEY" SECRET_KEY="$SECRET_KEY" juicefs format --storage s3 --bucket "$BUCKET" \
        --encrypt-rsa-key "$ETC/jfs-rsa.pem" --trash-days 0 "$META" "$VOLUME"
    fi
    unset ACCESS_KEY SECRET_KEY
    # Metadata is the filesystem: the unit backs it up into the bucket hourly (--backup-meta).
    # Losing $STATE/jfs.db without that backup loses the filesystem -- it is not optional.
    printf 'JFS_META=%s\nJFS_CACHE_MB=%s\n' "$META" "$((CACHE_GB*1024))" > "$ETC/juicefs.env"
    install -d -m 0700 "$WORKSPACES"
    install -m 0644 "$HERE/juicefs-nexal-storage.service" /etc/systemd/system/juicefs-nexal-storage.service
    systemctl daemon-reload
    systemctl enable --now juicefs-nexal-storage.service
    for i in $(seq 1 30); do mountpoint -q "$WORKSPACES" && break; sleep 1; done
    mountpoint -q "$WORKSPACES" || die "JuiceFS did not mount; see: journalctl -u juicefs-nexal-storage"
    own "$WORKSPACES"
    echo "Workspaces on S3 via JuiceFS: $WORKSPACES (bucket $BUCKET)"
    echo "Keep $ETC/jfs-rsa.pem safe and backed up separately -- without it the hourly"
    echo "metadata backup in the bucket cannot be decrypted."
    ;;
  *) echo "usage: $0 local|s3"; exit 64 ;;
esac
