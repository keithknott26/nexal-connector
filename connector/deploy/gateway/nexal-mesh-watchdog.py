#!/usr/bin/env python3
"""neXal mesh watchdog for the storage gateway (run every 30 s by nexal-mesh-watchdog.timer).

A mesh peer can end up "Connected" with a fresh WireGuard handshake while carrying no traffic, or stuck with
its ML-KEM session pending, until `netbird down && netbird up` rebuilds the session. This notices either and
does that bounce, at most once per COOLDOWN, after saving what it saw (so the cause can be fixed upstream):

  black hole  a connected peer we sent at least MIN_SENT bytes to over the last WINDOW seconds answered with no
              more than MAX_REPLY bytes (WireGuard keepalives alone stay under it; any TCP exchange does not)
  pending     a connected peer has reported a stuck ML-KEM lease (quantumReason session-pending,
              exchange-pending, peer-unreachable or evidence-expired) continuously for PENDING_FOR seconds.
              A gated session drops the peer's data while its lease is invalid, so the link looks up
              (Connected, fresh handshakes) but every connection times out.

Evidence goes to /var/log/nexal-mesh-watchdog/<time>-<peer>.log; one summary line per action to the journal.
Pass --dry-run to report without bouncing. Standard library only.
"""
import argparse
import json
import os
import subprocess
import sys
import time

STATE = "/var/lib/nexal-mesh-watchdog/state.json"
LOGDIR = "/var/log/nexal-mesh-watchdog"
WINDOW = 120          # seconds of counters compared for a black hole
MIN_SENT = 600        # bytes sent to the peer in WINDOW before silence counts: a client retrying TCP
                      # connects (a few SYNs a minute) must count; keepalives alone stay far below
MAX_REPLY = 400       # bytes back from it in WINDOW that still count as silence (keepalives)
PENDING_FOR = 180     # seconds a lease may stay stuck (leases last about 3 minutes)
COOLDOWN = 600        # seconds between bounces
PENDING = {"session-pending", "exchange-pending", "peer-unreachable", "evidence-expired"}


def status():
    out = subprocess.run(["netbird", "status", "--json"], capture_output=True, text=True, timeout=30)
    if out.returncode != 0:
        raise RuntimeError(out.stderr.strip()[:200] or "netbird status failed")
    return json.loads(out.stdout)


def peers(doc):
    return ((doc.get("peers") or {}).get("details")) or []


def decide(doc, state, now):
    """(peer name, reason) of the first peer that needs a bounce, or None. Updates state (history per peer)."""
    hist = state.setdefault("peers", {})
    seen = set()
    found = None
    for p in peers(doc):
        key = p.get("publicKey") or p.get("fqdn")
        if not key:
            continue
        seen.add(key)
        name = (p.get("fqdn") or key).split(".")[0]
        h = hist.setdefault(key, {"samples": [], "pending_since": None})
        if p.get("status") != "Connected":
            h["samples"], h["pending_since"] = [], None
            continue
        rx, tx = int(p.get("transferReceived") or 0), int(p.get("transferSent") or 0)
        samples = [s for s in h["samples"] if now - s[0] <= WINDOW + 60]
        if samples and (rx < samples[-1][1] or tx < samples[-1][2]):
            samples = []  # counters reset (reconnect, restart)
        samples.append([now, rx, tx])
        h["samples"] = samples
        old = next((s for s in samples if now - s[0] >= WINDOW), None)
        if old is not None and found is None:
            sent, back = tx - old[2], rx - old[1]
            if sent >= MIN_SENT and back <= MAX_REPLY:
                found = (name, f"black hole: sent {sent} B, got {back} B back in {int(now - old[0])} s")
        reason = p.get("quantumReason") or ""
        if reason in PENDING:
            h["pending_since"] = h["pending_since"] or now
            if found is None and now - h["pending_since"] >= PENDING_FOR:
                found = (name, f"ML-KEM session {reason} for {int(now - h['pending_since'])} s")
        else:
            h["pending_since"] = None
    for key in list(hist):
        if key not in seen:
            del hist[key]
    return found


def evidence(path, peer, reason):
    with open(path, "w") as f:
        f.write(f"{time.strftime('%Y-%m-%dT%H:%M:%S%z')} peer={peer} {reason}\n\n== netbird status -d\n")
        f.flush()
        subprocess.run(["netbird", "status", "-d"], stdout=f, stderr=subprocess.STDOUT, timeout=30)
        f.write("\n== netbird log (last 400 lines)\n")
        f.flush()
        for log in ("/var/log/netbird/client.log", "/var/log/netbird/netbird.err"):
            if os.path.exists(log):
                subprocess.run(["tail", "-n", "400", log], stdout=f, stderr=subprocess.STDOUT, timeout=30)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--dry-run", action="store_true", help="report, never bounce")
    a = ap.parse_args()
    os.makedirs(os.path.dirname(STATE), exist_ok=True)
    try:
        with open(STATE) as f:
            state = json.load(f)
    except (OSError, ValueError):
        state = {}
    now = time.time()
    try:
        doc = status()
    except Exception as e:  # the service is restarting, or down: nothing to judge
        print(f"status unavailable: {e}")
        return 0
    found = decide(doc, state, now)
    if found and now - state.get("last_bounce", 0) >= COOLDOWN:
        peer, reason = found
        os.makedirs(LOGDIR, mode=0o700, exist_ok=True)
        path = os.path.join(LOGDIR, time.strftime("%Y%m%d-%H%M%S") + f"-{peer}.log")
        evidence(path, peer, reason)
        if a.dry_run:
            print(f"would bounce the mesh: {peer}: {reason} (evidence {path})")
        else:
            print(f"bouncing the mesh: {peer}: {reason} (evidence {path})")
            subprocess.run(["netbird", "down"], timeout=60)
            subprocess.run(["netbird", "up"], timeout=120)
            state["last_bounce"] = now
            state["peers"] = {}  # counters restart with the new sessions
    elif found:
        print(f"{found[0]}: {found[1]} (bounced {int(now - state['last_bounce'])} s ago; waiting)")
    tmp = STATE + ".tmp"
    with open(tmp, "w") as f:
        json.dump(state, f)
    os.replace(tmp, STATE)
    return 0


if __name__ == "__main__":
    sys.exit(main())
