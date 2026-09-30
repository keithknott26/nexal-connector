#!/usr/bin/env python3
"""Prepare an isolated experimental source tree. Never installs or starts a VPN."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess

root = Path(__file__).resolve().parent
args = argparse.ArgumentParser(description=__doc__)
args.add_argument('--netbird-source', type=Path, required=True)
args.add_argument('--rosenpass-source', type=Path, required=True)
args.add_argument('--wireguard-source', type=Path, required=True)
args.add_argument('--output', type=Path, required=True)
opts = args.parse_args()
manifest = json.loads((root / 'upstream.json').read_text())
def digest(path):
    h = hashlib.sha256()
    for f in sorted(path.rglob('*')):
        if f.is_symlink():
            raise SystemExit('Symlinks in source are not supported')
        if f.is_file():
            h.update(f.relative_to(path).as_posix().encode() + b'\0' + f.read_bytes() + b'\0')
    return h.hexdigest()
for name, source in [('netbird', opts.netbird_source), ('rosenpass', opts.rosenpass_source), ('wireguard', opts.wireguard_source)]:
    if not source.is_dir() or digest(source) != manifest[name]['treeSha256']:
        raise SystemExit(f'{name} source does not match the pinned tree; refusing to patch')
opts.output.mkdir(parents=True, exist_ok=False)
for name, source in [('netbird', opts.netbird_source), ('rosenpass', opts.rosenpass_source), ('wireguard', opts.wireguard_source)]:
    destination = opts.output / name
    shutil.copytree(source, destination)
    destination.chmod(0o755)
    for f in destination.rglob('*'):
        f.chmod(f.stat().st_mode | 0o200)
    subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(root / f'{name}-mlkem1024.patch')], cwd=destination, check=True)
subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(root / 'netbird-health-budget.patch')], cwd=opts.output / 'netbird', check=True)
subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(root / 'rosenpass-retry.patch')], cwd=opts.output / 'rosenpass', check=True)
# iOS: run the ML-KEM control channel on an in-process stack inside the tunnel,
# because a Network Extension's own sockets never use its own tunnel. No-op elsewhere.
subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(root / 'wireguard-inject.patch')], cwd=opts.output / 'wireguard', check=True)
subprocess.run(['patch', '--batch', '--fuzz=0', '-p1', '-i', str(root / 'netbird-ios-control.patch')], cwd=opts.output / 'netbird', check=True)
for f in root.glob('nexal_*test.go'):
    shutil.copyfile(f, opts.output / 'rosenpass' / f.name)
module = opts.output / 'netbird' / 'go.mod'
replacement = 'replace golang.zx2c4.com/wireguard => github.com/netbirdio/wireguard-go v0.0.0-20260914123147-8bf8fa968f1a'
source = module.read_text()
if source.count(replacement) != 1:
    raise SystemExit('Pinned WireGuard module replacement was not found exactly once')
module.write_text(source.replace(replacement, 'replace golang.zx2c4.com/wireguard => ../wireguard'))
with module.open('a') as out:
    out.write('\n// Experimental neXal profile; NOT upstream wire-compatible.\nreplace cunicu.li/go-rosenpass => ../rosenpass\n')
shutil.copyfile(root / 'netbird_profile_test.go', opts.output / 'netbird/client/internal/rosenpass/nexal_profile_test.go')
shutil.copyfile(root / 'netbird_evidence_test.go', opts.output / 'netbird/client/internal/rosenpass/nexal_evidence_test.go')
shutil.copyfile(root / 'netbird_status_evidence_test.go', opts.output / 'netbird/client/status/nexal_evidence_test.go')
shutil.copyfile(root / 'netbird_gate_mock_test.go', opts.output / 'netbird/client/internal/rosenpass/nexal_gate_mock_test.go')
shutil.copyfile(root / 'netbird_endtoend_test.go', opts.output / 'netbird/client/internal/rosenpass/nexal_endtoend_test.go')
shutil.copyfile(root / 'wireguard_quantum_test.go', opts.output / 'wireguard/device/nexal_quantum_test.go')
print('Prepared isolated experimental sources. No installed runtime or production packaging changed.')

shutil.copyfile(root / "netbird_reconnect_test.go", opts.output / "netbird/client/internal/rosenpass/nexal_reconnect_test.go")
