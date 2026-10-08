cat > ~/gateway-audit.sh <<'AUDIT'
#!/bin/bash
OUT="$HOME/gateway-audit.txt"
CID=d557e5ffcd341a3995b938327ac108820cad59e04bbbfdf720162f8eecf5563b
ssh -o ConnectTimeout=15 ubuntu@mesh.nexal.systems "sudo bash -s" > "$OUT" 2>&1 <<REMOTE
CID=$CID
s(){ echo; echo "===== \$1 ====="; }
s "host";            hostname; uptime; date -u; uname -a
s "logged in";       who; echo; last -n 25 -a | head -30
s "docker ps -a";    docker ps -a --no-trunc --format '{{.ID}} | {{.Names}} | {{.Image}} | {{.Status}} | {{.Ports}} | {{.Command}}'
s "the container";   docker inspect \$CID --format 'name={{.Name}} image={{.Config.Image}} created={{.Created}} started={{.State.StartedAt}} running={{.State.Running}} restart={{.HostConfig.RestartPolicy.Name}} net={{.HostConfig.NetworkMode}} priv={{.HostConfig.Privileged}}
cmd={{json .Config.Cmd}} entrypoint={{json .Config.Entrypoint}}
ports={{json .NetworkSettings.Ports}}
mounts={{range .Mounts}}{{.Source}}->{{.Destination}} {{end}}
labels={{json .Config.Labels}}' 2>&1
s "log size";        ls -la /var/lib/docker/containers/\$CID/ 2>&1
s "log head (20)";   head -n 20 /var/lib/docker/containers/\$CID/\$CID-json.log 2>&1 | cut -c1-400
s "log tail (150)";  tail -n 150 /var/lib/docker/containers/\$CID/\$CID-json.log 2>&1 | cut -c1-400
s "log: top requests"; grep -oE '"(GET|POST|PUT|HEAD|OPTIONS|CONNECT|DELETE) [^ "]+' /var/lib/docker/containers/\$CID/\$CID-json.log 2>/dev/null | sort | uniq -c | sort -rn | head -40
s "log: top source IPs"; grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' /var/lib/docker/containers/\$CID/\$CID-json.log 2>/dev/null | sort | uniq -c | sort -rn | head -25
s "VMs";             (command -v virsh && virsh list --all) 2>&1; ps -eo pid,user,etime,cmd | grep -Ei 'qemu|kvm|firecracker|vmhost|lima|colima' | grep -v grep
s "listening ports"; ss -tulpn
s "processes (top cpu)"; ps -eo pid,user,etime,%cpu,%mem,cmd --sort=-%cpu | head -40
s "ssh auth (24h)";  journalctl -u ssh -u sshd --since '-24h' --no-pager 2>/dev/null | grep -Ei 'Accepted|Failed|Invalid' | awk '{\$1=\$2=\$3="";print}' | sed -E 's/port [0-9]+//' | sort | uniq -c | sort -rn | head -30
s "authorized_keys"; for f in /root/.ssh/authorized_keys /home/*/.ssh/authorized_keys; do [ -f \$f ] && { echo "\$f"; ssh-keygen -lf \$f 2>&1; }; done
s "cron";            ls -la /etc/cron.d /var/spool/cron/crontabs 2>&1; crontab -l -u root 2>&1; crontab -l -u ubuntu 2>&1
s "changed in 3 days"; find /usr/local/bin /usr/bin /etc /opt /root /home -xdev -type f -mtime -3 2>/dev/null | grep -vE '/etc/(ld.so.cache|resolv.conf)|\.cache/' | head -80
s "users with shells"; awk -F: '\$7 ~ /(bash|sh|zsh)$/ {print \$1, \$3, \$6, \$7}' /etc/passwd
s "ufw";             ufw status verbose 2>&1 | head -40
REMOTE
echo "Wrote $OUT ($(wc -l < "$OUT") lines)"
AUDIT
bash ~/gateway-audit.sh
