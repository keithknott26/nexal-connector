# nexal-vmhost smoke test (Debian 12 arm64, headless)

Run on the Mac in Terminal. Uses `/tmp/vmt` (short path for unix sockets).

## 1. Build
```sh
cd ~/path/to/nexal-connector/macos && scripts/build-vmhost.sh     # prints the binary path
export VMHOST="$(swift build -c release --show-bin-path)/nexal-vmhost"
$VMHOST --version
```

## 2. Image, seed, spec
```sh
mkdir -p /tmp/vmt/seed && cd /tmp/vmt
curl -LO https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-genericcloud-arm64.raw
mv debian-12-genericcloud-arm64.raw disk.raw && truncate -s 20G disk.raw   # grows on first boot

[ -f ~/.ssh/id_ed25519.pub ] || ssh-keygen -t ed25519 -N '' -f ~/.ssh/id_ed25519
cat > seed/meta-data <<EOT
instance-id: smoke-1
local-hostname: smoke
EOT
cat > seed/user-data <<EOT
#cloud-config
users:
  - name: nexal
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    shell: /bin/bash
    lock_passwd: true
    ssh_authorized_keys:
      - $(cat ~/.ssh/id_ed25519.pub)
EOT
hdiutil makehybrid -iso -joliet -default-volume-name cidata -o seed.iso seed

cat > spec.json <<EOT
{"sandboxId":"smoke","hostname":"smoke","cpus":2,"memoryMB":2048,
 "diskPath":"/tmp/vmt/disk.raw","seedPath":"/tmp/vmt/seed.iso",
 "consoleLog":"/tmp/vmt/console.log","controlSocket":"/tmp/vmt/c.sock",
 "guestSocket":"/tmp/vmt/g.sock","desktop":false,"keepAwake":false}
EOT
```

## 3. Boot under launchd (no terminal, no window)
```sh
cat > /tmp/vmt/vm.plist <<EOT
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>systems.nexal.vmhost.smoke</string>
<key>ProgramArguments</key><array><string>$VMHOST</string><string>run</string><string>--spec</string><string>/tmp/vmt/spec.json</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><false/>
<key>StandardErrorPath</key><string>/tmp/vmt/host.log</string>
<key>StandardOutPath</key><string>/tmp/vmt/host.log</string>
</dict></plist>
EOT
launchctl bootstrap gui/$(id -u) /tmp/vmt/vm.plist
sleep 45; cat /tmp/vmt/host.log; tail -5 /tmp/vmt/console.log
```
Expect: empty host.log, console shows boot/cloud-init. `launchctl print gui/$(id -u)/systems.nexal.vmhost.smoke | grep state`.

**Boots without a logged-in user:** lock the screen (Ctrl-Cmd-Q) or switch to another user via
fast user switching, wait, then check the console log keeps progressing and step 4 still works.
(A full logout kills the gui/<uid> domain; the connector's LaunchAgent has the same limit.)

## 4. SSH over NAT
```sh
IP=$(awk '/name=smoke/{f=1} f&&/ip_address/{sub("ip_address=","");print;exit}' /var/db/dhcpd_leases)
echo $IP; ssh -o StrictHostKeyChecking=no nexal@$IP 'hostname; uname -m; df -h /'
```
(Fallback: `grep -i "ip\|eth\|enp" /tmp/vmt/console.log`.) Root disk should be ~20G.

## 5. Control socket and stop
```sh
$VMHOST stop --control /tmp/vmt/c.sock; echo "stop rc=$?"      # returns immediately, 0
sleep 15; launchctl print gui/$(id -u)/systems.nexal.vmhost.smoke 2>&1 | grep -E "state|last exit"
```
Expect the job to exit 0 (not running). If you boot again before this, `status` works:
`printf 'status\n' | nc -U /tmp/vmt/c.sock` prints `running`.

## 6. Persistence across restart
```sh
ssh nexal@$IP 'echo persisted > ~/marker; cat /etc/machine-id'   # before stopping (repeat 3-5)
launchctl bootout gui/$(id -u)/systems.nexal.vmhost.smoke 2>/dev/null
rm -f /tmp/vmt/seed.iso && sed -i '' 's/"seedPath":"[^"]*"/"seedPath":""/' /tmp/vmt/spec.json
launchctl bootstrap gui/$(id -u) /tmp/vmt/vm.plist; sleep 40
ssh nexal@$IP 'cat ~/marker; cat /etc/machine-id'
ls /tmp/vmt/disk.raw.*        # .efivars .machine-id .mac
```
Expect `persisted`, the same machine-id and same IP (stable MAC), no seed attached.

## 7. Pauses with Mac sleep
```sh
ssh nexal@$IP 'while true; do date +%s; sleep 1; done' > /tmp/vmt/ticks.txt &
sleep 10; pmset sleepnow      # wake the Mac after ~1 minute
```
After wake, `awk 'NR>1&&$1-p>5{print "gap",$1-p}{p=$1}' /tmp/vmt/ticks.txt` shows no
gap (the loop stalled while suspended, then continues), while `date` on the Mac vs
`ssh nexal@$IP date` may differ by the sleep duration until the guest re-syncs its clock
(expected; `ssh nexal@$IP 'sudo systemctl restart systemd-timesyncd'` or install chrony). Confirm the
process has no assertion: `pmset -g assertions | grep -i vmhost` prints nothing.

## Cleanup
```sh
launchctl bootout gui/$(id -u)/systems.nexal.vmhost.smoke; rm -rf /tmp/vmt
```
