#!/bin/bash
# Installs a container runtime that dev-container throwaway hosts can use.
# Docker Desktop is refused by the connector (`docker_desktop_only`), so this
# sets up Colima (MIT) plus the docker and devpod command-line tools, via
# Homebrew, and keeps Colima running across logins with `brew services`.
#
# Idempotent: exits 0 at once if OrbStack, Colima or Lima is already serving.
# Never uses sudo. Last line of output is a one-line status the app shows.
# Exit codes: 0 ready, 3 Homebrew missing, 1 anything else.
set -uo pipefail
export PATH="/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
export HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_INSTALL_CLEANUP=1 HOMEBREW_NO_ENV_HINTS=1

say() { printf '%s\n' "$*"; }

# Colima run as root puts its socket under /var/root, where the connector (running
# as the signed-in user) can never reach it, and leaves root-owned Homebrew paths.
if [ "$(id -u)" -eq 0 ]; then
  say "Run this as your own user, not with sudo: a root Colima is invisible to the connector."
  exit 1
fi

# `brew services start` fails with "Bootstrap failed: 5" when a stale
# sh.brew.colima job is still loaded; unload it and try once more.
start_colima() {
  brew services start colima >/dev/null 2>&1 && return 0
  launchctl bootout "gui/$(id -u)/sh.brew.colima" >/dev/null 2>&1 || true
  sleep 1
  brew services start colima >/dev/null 2>&1 && return 0
  say "brew services could not start Colima; starting it for this session only."
  colima start >/dev/null 2>&1
}

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

need_devpod() {
  command -v devpod >/dev/null 2>&1 && return 0
  say "Installing devpod…"
  brew install devpod
}

if serving; then
  if command -v devpod >/dev/null 2>&1; then exit 0; fi
  command -v brew >/dev/null 2>&1 || { say "devpod is missing; install Homebrew (https://brew.sh), then try again."; exit 3; }
  need_devpod && { say "Container runtime ready; devpod installed."; exit 0; }
  say "Installing devpod failed."; exit 1
fi

if [ -d /Applications/OrbStack.app ]; then
  say "OrbStack is installed but not running; starting it…"
  open -g -a OrbStack || true
  for _ in $(seq 1 30); do serving && break; sleep 2; done
  if serving >/dev/null; then
    command -v brew >/dev/null 2>&1 && need_devpod
    command -v devpod >/dev/null 2>&1 && { say "Container runtime ready (OrbStack)."; exit 0; }
    say "OrbStack is running but devpod is missing; install Homebrew (https://brew.sh), then try again."; exit 3
  fi
fi

if ! command -v brew >/dev/null 2>&1; then
  say "Dev containers need Colima or OrbStack. Install Homebrew (https://brew.sh) or OrbStack (https://orbstack.dev), then try again."
  exit 3
fi

say "Installing Colima…"
brew install colima || { say "Installing Colima failed."; exit 1; }
if ! command -v docker >/dev/null 2>&1; then
  say "Installing the docker command-line tool…"
  brew install docker || { say "Installing the docker command-line tool failed."; exit 1; }
fi
need_devpod || { say "Installing devpod failed."; exit 1; }

say "Starting Colima (first start downloads a small Linux image)…"
# brew services keeps Colima running after logout and restart.
start_colima || true
for _ in $(seq 1 90); do
  if serving >/dev/null; then say "Container runtime ready (Colima)."; exit 0; fi
  sleep 2
done
say "Colima was installed but did not start; run 'colima start' in Terminal to see why."
exit 1
