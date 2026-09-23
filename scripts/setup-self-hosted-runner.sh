#!/usr/bin/env bash
# Register this Mac as the self-hosted runner for nexal-connector CI.
#
# Run this ON THE MAC that should build the app (the M4 mini or the Studio), not in
# a sandbox and not on a laptop that sleeps. It needs the `gh` CLI signed in as the
# repository owner; the registration token is fetched here and never leaves the
# machine.
#
#   bash scripts/setup-self-hosted-runner.sh
#
# WHY SELF-HOSTED. GitHub bills hosted macOS at a 10x multiplier. In September an
# unrelated repo exhausted the shared Pro allowance and GitHub then refused to start
# hosted jobs at all. For THIS repo the consequence is specific: the Mac app's Swift
# sources are edited without a Swift toolchain available, and the mac job was gated to
# manual runs, so nothing compiled them on a push. A self-hosted runner consumes no
# allowance and lets that job run on every push.
set -euo pipefail

# On a personal account there are no organization-wide runners, so a runner is
# registered PER REPOSITORY. One Mac can host several; each needs its own directory
# and its own registration. Run this once per repo:
#
#   bash scripts/setup-self-hosted-runner.sh                              # nexal-ios
#   REPO=keithknott26/nexal-platform  bash scripts/setup-self-hosted-runner.sh
#   REPO=keithknott26/nexal-connector bash scripts/setup-self-hosted-runner.sh
REPO="${REPO:-keithknott26/nexal-connector}"
# Derived from the repo, not fixed: passing REPO without also remembering to change
# the directory would otherwise collide with an existing runner's registration.
RUNNER_DIR="${RUNNER_DIR:-$HOME/actions-runner-${REPO##*/}}"
# The repo is in the name for the same reason -- several runners on one Mac are
# indistinguishable in the GitHub UI otherwise.
#
# The name is SANITISED first. svc.sh builds both the launchd label and the plist
# FILENAME from it, and a Mac named "Keith's Mac Mini M4" therefore produces a label
# containing an apostrophe. launchd reports that as "Load failed: 5: Input/output
# error" -- which says nothing about the cause -- and every later svc.sh invocation
# needs careful quoting. Spaces become underscores anyway, so only the characters that
# actually break things are replaced: anything outside [A-Za-z0-9._-].
RAW_NAME="${RUNNER_NAME:-$(scutil --get ComputerName 2>/dev/null || hostname)-${REPO##*/}}"
RUNNER_NAME="$(printf '%s' "$RAW_NAME" | LC_ALL=C tr -c 'A-Za-z0-9._-' '-' | tr -s '-' | sed 's/^-//;s/-$//')"
if [ "$RUNNER_NAME" != "$RAW_NAME" ]; then
  echo "Runner name sanitised for launchd: '$RAW_NAME' -> '$RUNNER_NAME'"
fi
[ -n "$RUNNER_NAME" ] || { echo "Could not derive a usable runner name."; exit 1; }

command -v gh >/dev/null 2>&1 || { echo "gh CLI is required: brew install gh"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "Sign in first: gh auth login"; exit 1; }

# The runner must be Apple Silicon: the workflow asks for the ARM64 label, and an
# Intel Mac would register as X64 and never pick up a job.
if [ "$(uname -m)" != "arm64" ]; then
  echo "This Mac reports $(uname -m). The workflow targets ARM64 (Apple Silicon)."
  exit 1
fi

xcode-select -p >/dev/null 2>&1 || { echo "Xcode command line tools missing: xcode-select --install"; exit 1; }
# No xcodegen here: macos/ is a SwiftPM package built with `swift build`, not a
# generated Xcode project. `swift` ships with the command line tools checked above.
command -v swift >/dev/null 2>&1 || { echo "swift not found; install Xcode or the command line tools"; exit 1; }

if [ -d "$RUNNER_DIR" ]; then
  echo "Runner directory already exists at $RUNNER_DIR."
  echo "To re-register, remove it first: cd $RUNNER_DIR && sudo ./svc.sh uninstall && cd .. && rm -rf $RUNNER_DIR"
  exit 1
fi

echo "Fetching the latest runner release"
VERSION="$(gh api repos/actions/runner/releases/latest -q .tag_name | sed 's/^v//')"
TARBALL="actions-runner-osx-arm64-${VERSION}.tar.gz"

mkdir -p "$RUNNER_DIR"
cd "$RUNNER_DIR"
curl -fsSL -o "$TARBALL" \
  "https://github.com/actions/runner/releases/download/v${VERSION}/${TARBALL}"
tar xzf "$TARBALL"
rm -f "$TARBALL"

# Short-lived (one hour) and scoped to this repository. Fetched here so it is never
# pasted into a chat window or a shell history on another machine.
echo "Requesting a registration token"
TOKEN="$(gh api -X POST "repos/${REPO}/actions/runners/registration-token" -q .token)"

# --labels is deliberately omitted: a macOS ARM runner applies `self-hosted`, `macOS`
# and `ARM64` automatically, which is exactly what ci.yml asks for. Adding custom
# labels here would silently stop matching that workflow.
./config.sh \
  --url "https://github.com/${REPO}" \
  --token "$TOKEN" \
  --name "$RUNNER_NAME" \
  --work _work \
  --unattended \
  --replace

# Installed as a launchd service rather than run in a terminal, so the runner
# survives logout and reboot. A runner that only exists while a terminal window is
# open is how "CI is green" quietly becomes "CI never ran".
echo "Installing the runner as a background service"
./svc.sh install
./svc.sh start

echo
echo "Registered as '${RUNNER_NAME}'."
echo "Check it: gh api repos/${REPO}/actions/runners -q '.runners[] | \"\(.name) \(.status)\"'"
echo
echo "KEEP THIS MAC AWAKE, or jobs will queue instead of running:"
echo "  sudo pmset -a sleep 0 disablesleep 1     # desktop Macs only"
echo
echo "Stop or remove later:"
echo "  cd $RUNNER_DIR && ./svc.sh stop"
echo "  cd $RUNNER_DIR && ./svc.sh uninstall && ./config.sh remove --token \$(gh api -X POST repos/${REPO}/actions/runners/remove-token -q .token)"
