#!/usr/bin/env python3
"""Fail closed before packaging or installing a neXal networking runtime."""
import argparse
import hashlib
import json
import pathlib
import subprocess
import sys

POLICY = json.loads(pathlib.Path(__file__).with_name('runtime-policy.json').read_text())

def run(*args):
    return subprocess.run([str(a) for a in args], check=True, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout.strip()

def verify_source(path):
    path = pathlib.Path(path)
    if path.is_symlink() or not path.is_file():
        raise ValueError('Runtime source must be a regular file, not a symlink')
    if hashlib.sha256(path.read_bytes()).hexdigest() != POLICY['source_sha256']:
        raise ValueError('Runtime checksum is not approved; stock or unreviewed builds cannot be packaged')

def verify_app(app):
    app = pathlib.Path(app)
    if app.is_symlink():
        raise ValueError('App must not be a symlink')
    # Validate signatures BEFORE executing anything in the candidate bundle.
    run('/usr/bin/codesign', '--verify', '--deep', '--strict', app)
    requirement = '=anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists and certificate leaf[subject.OU] = "' + POLICY['team_id'] + '"'
    run('/usr/bin/codesign', '--verify', '-R', requirement + ' and identifier "' + POLICY['bundle_id'] + '"', app)
    for relative in ['MacOS/NexalMac', 'Helpers/nexal', 'Helpers/nexal-network', 'Helpers/nexal-vmhost']:
        binary = app / 'Contents' / relative
        if binary.is_symlink():
            raise ValueError('Bundled executables must not be symlinks')
        run('/usr/bin/codesign', '--verify', '--strict', '-R', requirement, binary)
        if set(run('/usr/bin/lipo', '-archs', binary).split()) != {'arm64', 'x86_64'}:
            raise ValueError('Both Mac architectures are required: ' + relative)
    # Virtualization.framework refuses to start a VM without this entitlement; a bundle that
    # lacks it (or lacks the helper) would report "nexal-vmhost is not installed" on every Mac.
    entitlements = subprocess.run(['/usr/bin/codesign', '-d', '--entitlements', ':-', app / 'Contents/Helpers/nexal-vmhost'],
                                  capture_output=True, text=True).stdout
    if 'com.apple.security.virtualization' not in entitlements:
        raise ValueError('nexal-vmhost is missing the virtualization entitlement')
    version = run(app / 'Contents/Helpers/nexal-network', 'version')
    if version != POLICY['version']:
        raise ValueError('Unapproved runtime version: ' + version)

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=['source', 'app'])
    parser.add_argument('path')
    args = parser.parse_args()
    try:
        (verify_source if args.mode == 'source' else verify_app)(args.path)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print('Runtime verification failed: ' + str(error), file=sys.stderr)
        sys.exit(1)
    print('Approved custom ML-KEM runtime verified.')
