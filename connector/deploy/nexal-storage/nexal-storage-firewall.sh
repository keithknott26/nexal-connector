#!/bin/sh
# Host firewall for neXal storage tenant containers (iptables; run as root at boot,
# e.g. from a oneshot unit After=docker.service). Every tenant network's bridge is
# named nx-<12 hex>, so "nx-+" matches all of them and nothing else.
#
#  1. Containers may not open connections to the server itself (SSH, SMB for Time
#     Machine, the Docker API, ...): INPUT from any tenant bridge is dropped except
#     replies and DNS to Docker's embedded resolver (which lives inside the
#     container's namespace and does not traverse INPUT anyway).
#  2. Containers may not reach private, link-local or CGNAT ranges (the LAN, cloud
#     metadata, other mesh peers outside the sidecar's own tunnel). Public internet
#     stays open (package installs, git, the mesh control plane and relays).
#  3. Tenant bridges cannot reach each other (Docker already isolates bridge
#     networks; the rule below makes it explicit).
set -eu
ipt=iptables
$ipt -N NEXAL-TENANTS 2>/dev/null || $ipt -F NEXAL-TENANTS
$ipt -A NEXAL-TENANTS -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
$ipt -A NEXAL-TENANTS -o nx-+ -j DROP
for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16 100.64.0.0/10 224.0.0.0/4; do
  $ipt -A NEXAL-TENANTS -d "$net" -j DROP
done
$ipt -A NEXAL-TENANTS -j RETURN
# DOCKER-USER is evaluated before Docker's own forwarding rules.
$ipt -C DOCKER-USER -i nx-+ -j NEXAL-TENANTS 2>/dev/null || $ipt -I DOCKER-USER -i nx-+ -j NEXAL-TENANTS
# Traffic to the server itself.
$ipt -C INPUT -i nx-+ -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT 2>/dev/null || $ipt -I INPUT -i nx-+ -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
$ipt -C INPUT -i nx-+ -j DROP 2>/dev/null || $ipt -A INPUT -i nx-+ -j DROP
echo "neXal tenant firewall applied"
