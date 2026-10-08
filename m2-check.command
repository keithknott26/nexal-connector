#!/bin/bash
# Read-only: why this Mac cannot reach the dev container over the mesh. Output: m2-check.txt next to this file.
cd "$(dirname "$0")"; exec > m2-check.txt 2>&1
s(){ echo; echo "===== $1 ====="; }
H=/Applications/neXal-Connector.app/Contents/Helpers
s "this Mac's view of sbx peers"; "$H/nexal-network" status --detail 2>&1 | awk '/sbx-/{p=1} p&&/^ *$/{p=0} p' | head -60
s "ping container"; ping -c3 -t5 100.86.63.60 2>&1 | tail -3
s "ssh port"; nc -z -G 5 -v 100.86.63.60 22 2>&1
s "containers"; docker ps -a --format '{{.Names}} | {{.Image}} | {{.Status}}' 2>&1 | head
for c in $(docker ps --format '{{.Names}}' | grep -iE 'mesh|sidecar|nexal'); do
  s "sidecar $c: netbird status"; docker exec "$c" netbird status --detail 2>&1 | head -70
  s "sidecar $c: log tail"; docker logs --tail 40 "$c" 2>&1 | cut -c1-260
  s "sidecar $c: client.log errors"; docker exec "$c" sh -c 'grep -hE "ERRO|WARN" /var/log/nexal/netbird.log 2>/dev/null | tail -25' 2>&1 | cut -c1-260
done
s "colima network"; colima list 2>&1; colima ssh -- ip -4 addr 2>&1 | grep inet
echo; echo DONE
