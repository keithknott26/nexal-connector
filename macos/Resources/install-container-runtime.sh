#!/bin/bash
# Makes sure this Mac has a container runtime for dev-container throwaway hosts.
# Docker Desktop is refused by the connector (`docker_desktop_only`), so this sets
# up Colima (MIT) with Lima (Apache-2.0), the docker CLI (Apache-2.0) and devpod
# (MPL-2.0, used unmodified as an external program).
#
# Order: a runtime that already answers -> OrbStack -> an existing Colima ->
# Homebrew -> neXal-managed install. The managed install needs NO Homebrew: it
# downloads pinned upstream releases into
#   ~/Library/Application Support/Nexal/runtime
# and keeps Colima running across logins with a per-user LaunchAgent.
#
# Integrity: GitHub release assets (Colima, Lima, devpod) are checked against the
# SHA-256 digest GitHub publishes for each asset; Lima also against its SHA256SUMS.
# The docker CLI comes from download.docker.com over HTTPS (Docker publishes no
# checksum file); set NEXAL_DOCKER_SHA256_ARM64 / _X86_64 to pin it.
#
# Idempotent. Never uses sudo. Last line of output is a one-line status the app
# shows. Exit codes: 0 ready, 1 failure.
set -uo pipefail

COLIMA_VERSION="v0.10.3"
LIMA_VERSION="2.2.0"
DEVPOD_VERSION="v0.6.15"
DOCKER_VERSION="29.8.2"

RT="$HOME/Library/Application Support/Nexal/runtime"
AGENT_LABEL="systems.nexal.colima"
AGENT_PLIST="$HOME/Library/LaunchAgents/$AGENT_LABEL.plist"
export PATH="$RT/bin:/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_INSTALL_CLEANUP=1 HOMEBREW_NO_ENV_HINTS=1

say() { printf '%s\n' "$*"; }

# Colima run as root puts its socket under /var/root, where the connector (running
# as the signed-in user) can never reach it.
if [ "$(id -u)" -eq 0 ]; then
  say "Run this as your own user, not with sudo: a root Colima is invisible to the connector."
  exit 1
fi

serving() {
  command -v docker >/dev/null 2>&1 || return 1
  local sock
  for sock in "$HOME/.orbstack/run/docker.sock" "$HOME/.colima/docker.sock" "$HOME"/.colima/*/docker.sock "$HOME/.lima/docker/sock/docker.sock"; do
    [ -S "$sock" ] || continue
    if docker --host "unix://$sock" version --format '{{.Server.Version}}' >/dev/null 2>&1; then
      say "Container runtime ready ($sock)."
      return 0
    fi
  done
  return 1
}

wait_serving() { # seconds
  local i
  for i in $(seq 1 $(( $1 / 2 ))); do serving >/dev/null && return 0; sleep 2; done
  return 1
}

# ---------- neXal-managed install (no Homebrew) ----------

case "$(uname -m)" in
  arm64) A_COLIMA=arm64; A_LIMA=arm64; A_DEVPOD=arm64; A_DOCKER=aarch64; DOCKER_PIN="${NEXAL_DOCKER_SHA256_ARM64:-}" ;;
  x86_64) A_COLIMA=x86_64; A_LIMA=x86_64; A_DEVPOD=amd64; A_DOCKER=x86_64; DOCKER_PIN="${NEXAL_DOCKER_SHA256_X86_64:-}" ;;
  *) say "Unsupported Mac architecture $(uname -m)."; exit 1 ;;
esac

TMP=""
cleanup() { [ -n "$TMP" ] && rm -rf "$TMP"; }
trap cleanup EXIT

fetch() { # url dest
  /usr/bin/curl --fail --location --silent --show-error --proto '=https' --tlsv1.2 \
    --retry 3 --connect-timeout 20 --max-time 900 -o "$2" "$1"
}

sha256_of() { /usr/bin/shasum -a 256 "$1" | awk '{print $1}'; }

# SHA-256 GitHub records for one release asset ("" if unavailable).
github_digest() { # owner/repo tag asset
  local json="$TMP/release-$(echo "$1$2" | tr '/' '_').json"
  [ -s "$json" ] || fetch "https://api.github.com/repos/$1/releases/tags/$2" "$json" || return 0
  /usr/bin/osascript -l JavaScript - "$json" "$3" <<'JS' 2>/dev/null
function run(argv) {
  const d = $.NSString.stringWithContentsOfFileEncodingError(argv[0], $.NSUTF8StringEncoding, null).js;
  const a = (JSON.parse(d).assets || []).find(x => x.name === argv[1]);
  return a && typeof a.digest === "string" && a.digest.startsWith("sha256:") ? a.digest.slice(7) : "";
}
JS
}

verify() { # file expected-sha256 label
  local got; got="$(sha256_of "$1")"
  if [ -z "$2" ]; then say "No published checksum for $3; refusing to install it."; return 1; fi
  if [ "$got" != "$2" ]; then say "Checksum mismatch for $3; not installed."; return 1; fi
}

fetch_github() { # owner/repo tag asset dest label
  fetch "https://github.com/$1/releases/download/$2/$3" "$4" || { say "Could not download $5."; return 1; }
  verify "$4" "$(github_digest "$1" "$2" "$3")" "$5"
}

managed_install() {
  TMP="$(mktemp -d -t nexal-runtime)" || return 1
  mkdir -p "$RT/bin" || return 1

  if [ ! -x "$RT/bin/limactl" ] || ! "$RT/bin/limactl" --version 2>/dev/null | grep -q " $LIMA_VERSION"; then
    say "Downloading Lima $LIMA_VERSION…"
    local lima="lima-$LIMA_VERSION-Darwin-$A_LIMA.tar.gz"
    fetch_github lima-vm/lima "v$LIMA_VERSION" "$lima" "$TMP/$lima" "Lima" || return 1
    # Lima also signs its own checksum list; both must agree.
    fetch "https://github.com/lima-vm/lima/releases/download/v$LIMA_VERSION/SHA256SUMS" "$TMP/SHA256SUMS" || { say "Could not download Lima checksums."; return 1; }
    grep -q "^$(sha256_of "$TMP/$lima")  $lima\$" "$TMP/SHA256SUMS" || { say "Lima checksum list does not match; not installed."; return 1; }
    rm -rf "$RT/share/lima"
    /usr/bin/tar -xzf "$TMP/$lima" -C "$RT" || { say "Could not unpack Lima."; return 1; }
  fi

  if [ ! -x "$RT/bin/colima" ] || ! "$RT/bin/colima" version 2>/dev/null | grep -q "${COLIMA_VERSION#v}"; then
    say "Downloading Colima $COLIMA_VERSION…"
    fetch_github abiosoft/colima "$COLIMA_VERSION" "colima-Darwin-$A_COLIMA" "$TMP/colima" "Colima" || return 1
    install -m 755 "$TMP/colima" "$RT/bin/colima" || return 1
  fi

  if [ ! -x "$RT/bin/docker" ]; then
    say "Downloading the docker command-line tool $DOCKER_VERSION…"
    fetch "https://download.docker.com/mac/static/stable/$A_DOCKER/docker-$DOCKER_VERSION.tgz" "$TMP/docker.tgz" \
      || { say "Could not download the docker command-line tool."; return 1; }
    if [ -n "$DOCKER_PIN" ]; then verify "$TMP/docker.tgz" "$DOCKER_PIN" "docker" || return 1; fi
    /usr/bin/tar -xzf "$TMP/docker.tgz" -C "$TMP" docker/docker || { say "Could not unpack docker."; return 1; }
    install -m 755 "$TMP/docker/docker" "$RT/bin/docker" || return 1
  fi

  if [ ! -x "$RT/bin/devpod" ]; then
    say "Downloading devpod $DEVPOD_VERSION…"
    fetch_github loft-sh/devpod "$DEVPOD_VERSION" "devpod-darwin-$A_DEVPOD" "$TMP/devpod" "devpod" || return 1
    install -m 755 "$TMP/devpod" "$RT/bin/devpod" || return 1
  fi
  return 0
}

# Keeps the managed Colima running for this user (what `brew services` does for a
# Homebrew install). vz is Apple's Virtualization framework: nothing else to install.
install_agent() {
  mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs/Nexal" || return 1
  cat > "$AGENT_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$AGENT_LABEL</string>
  <key>ProgramArguments</key><array>
    <string>$RT/bin/colima</string><string>start</string><string>--foreground</string>
    <string>--vm-type</string><string>vz</string><string>--mount-type</string><string>virtiofs</string>
  </array>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>$RT/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>StandardOutPath</key><string>$HOME/Library/Logs/Nexal/colima.log</string>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/Nexal/colima.log</string>
</dict></plist>
PLIST
  launchctl bootout "gui/$(id -u)/$AGENT_LABEL" >/dev/null 2>&1 || true
  launchctl bootstrap "gui/$(id -u)" "$AGENT_PLIST" >/dev/null 2>&1 \
    || launchctl kickstart -k "gui/$(id -u)/$AGENT_LABEL" >/dev/null 2>&1
}

# ---------- decide ----------

if serving; then
  command -v devpod >/dev/null 2>&1 && exit 0
  say "Installing devpod…"
  TMP="$(mktemp -d -t nexal-runtime)" && mkdir -p "$RT/bin" \
    && fetch_github loft-sh/devpod "$DEVPOD_VERSION" "devpod-darwin-$A_DEVPOD" "$TMP/devpod" "devpod" \
    && install -m 755 "$TMP/devpod" "$RT/bin/devpod" && { say "Container runtime ready; devpod installed."; exit 0; }
  say "Installing devpod failed."; exit 1
fi

if [ -d /Applications/OrbStack.app ]; then
  say "OrbStack is installed but not running; starting it…"
  open -g -a OrbStack || true
  if wait_serving 60; then
    command -v devpod >/dev/null 2>&1 || managed_install || exit 1
    say "Container runtime ready (OrbStack)."; exit 0
  fi
fi

# The managed install is used whenever it exists, even if Homebrew appeared later.
if [ -x "$RT/bin/colima" ] || ! command -v brew >/dev/null 2>&1; then
  managed_install || exit 1
  say "Starting Colima (the first start downloads a small Linux image)…"
  install_agent || { say "Could not register Colima to start at login."; exit 1; }
  if wait_serving 240; then say "Container runtime ready (Colima)."; exit 0; fi
  say "Colima was installed but did not start; see ~/Library/Logs/Nexal/colima.log."
  exit 1
fi

# Homebrew path (unchanged behaviour for Macs that already use Homebrew).
start_colima() {
  brew services start colima >/dev/null 2>&1 && return 0
  # "Bootstrap failed: 5": a stale sh.brew.colima job is still loaded.
  launchctl bootout "gui/$(id -u)/sh.brew.colima" >/dev/null 2>&1 || true
  sleep 1
  brew services start colima >/dev/null 2>&1 && return 0
  say "brew services could not start Colima; starting it for this session only."
  colima start >/dev/null 2>&1
}
command -v colima >/dev/null 2>&1 || { say "Installing Colima…"; brew install colima || { say "Installing Colima failed."; exit 1; }; }
command -v docker >/dev/null 2>&1 || { say "Installing the docker command-line tool…"; brew install docker || { say "Installing the docker command-line tool failed."; exit 1; }; }
command -v devpod >/dev/null 2>&1 || { say "Installing devpod…"; brew install devpod || { say "Installing devpod failed."; exit 1; }; }
say "Starting Colima (the first start downloads a small Linux image)…"
start_colima || true
if wait_serving 180; then say "Container runtime ready (Colima)."; exit 0; fi
say "Colima was installed but did not start; run 'colima start' in Terminal to see why."
exit 1
