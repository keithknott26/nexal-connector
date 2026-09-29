#!/usr/bin/env python3
"""Assemble an explicit local UAT app using a hash-verified experimental runtime."""
import argparse, hashlib, json, pathlib, plistlib, shutil, subprocess
p=argparse.ArgumentParser()
p.add_argument('--artifacts',type=pathlib.Path,required=True)
p.add_argument('--output',type=pathlib.Path,required=True)
p.add_argument('--identity',required=True)
p.add_argument('--build-number',default='20260927.1')
a=p.parse_args()
a.artifacts=a.artifacts.resolve(); a.output=a.output.resolve()
root=pathlib.Path(__file__).resolve().parents[2]
if a.output.exists(): raise SystemExit('Output must be new; existing builds are preserved.')
subprocess.run(['python3', str(root/'macos/scripts/verify-runtime.py'), 'source', str(a.artifacts/'nexal-network')], check=True)
manifest=json.loads((a.artifacts/'manifest.json').read_text())
if manifest['profile']!='nexal-mlkem1024-tcp-v2': raise SystemExit('Wrong runtime profile')
runtime=a.artifacts/'nexal-network'
if runtime.is_symlink() or hashlib.sha256(runtime.read_bytes()).hexdigest()!=manifest['artifacts']['nexal-network']: raise SystemExit('Runtime checksum mismatch')
a.output.mkdir(parents=True)
app=a.output/'neXal-Connector.app'
helpers=app/'Contents/Helpers';helpers.mkdir(parents=True)
resources=app/'Contents/Resources';resources.mkdir()
macos=app/'Contents/MacOS';macos.mkdir()
def run(*args,cwd=None): subprocess.run([str(x) for x in args],cwd=cwd,check=True)
scratch=a.output/'swift-build'
run('swift','build','--scratch-path',scratch,'-c','release','--arch','arm64','--arch','x86_64',cwd=root/'macos')
shutil.copy2(scratch/'out/Products/Release/NexalMac',macos/'NexalMac')
for arch in ['arm64','amd64']:
 import os
 env={**os.environ,'CGO_ENABLED':'0','GOOS':'darwin','GOARCH':arch}
 subprocess.run(['go','build','-trimpath','-ldflags=-s -w','-o',str(a.output/('nexal-'+arch)),'./cmd/nexal'],cwd=root/'connector',env=env,check=True)
run('/usr/bin/lipo','-create',a.output/'nexal-arm64',a.output/'nexal-amd64','-output',helpers/'nexal')
shutil.copy2(runtime,helpers/'nexal-network')
# Reuse only declarative resources from the source tree.
# No configuration, credentials, user data or previous app executables are copied.
for item in (root/'macos/Resources').iterdir():
 if item.is_file() and item.suffix in ['.txt','.icns']: shutil.copy2(item,resources/item.name)
info=plistlib.loads((root/'macos/Resources/Info.plist').read_bytes())
info['CFBundleVersion']=a.build_number
(app/'Contents/Info.plist').write_bytes(plistlib.dumps(info))
for file in [helpers/'nexal',helpers/'nexal-network',macos/'NexalMac']:
 file.chmod(0o755)
 args=['/usr/bin/codesign','--force','--options','runtime','--timestamp','--sign',a.identity]
 run(*args,file)
run('/usr/bin/codesign','--force','--options','runtime','--timestamp','--sign',a.identity,app)
run('/usr/bin/codesign','--verify','--deep','--strict',app)
run('python3',root/'macos/scripts/verify-runtime.py','app',app)
result={'profile':manifest['profile'],'runtimeVersion':manifest.get('runtimeVersion',manifest.get('version')),'notarized':False,'installed':False,'files':{str(f.relative_to(app)):hashlib.sha256(f.read_bytes()).hexdigest() for f in app.rglob('*') if f.is_file()}}
(a.output/'manifest.json').write_text(json.dumps(result,indent=2)+'\n')
print(app)
