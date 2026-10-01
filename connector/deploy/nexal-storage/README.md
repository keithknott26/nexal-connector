# neXal storage: managed dev-container runner

This turns K's Linux storage server into **neXal storage**, the location members can pick in the
Create-instance picker (iOS and dashboard) for dev containers, persistent or not. VMs on it are later
work (Linux QEMU/KVM, ticket 5 in `nexal-platform/docs/NEXAL-CLOUD-HOSTS.md`).

What runs: the ordinary `nexal` connector, enrolled as a host in the **operator's** account and flagged
as a managed host on the coordinator. It polls its own task queue with its own host token. Tasks come
from every member's account; each sandbox stays in its member's account and network. The server
itself never joins a member's network: each container's mesh sidecar joins the member's network with
a one-use key made for that sandbox.

## Isolation between members on this server

| Layer | What separates tenants |
|---|---|
| Coordinator | Each task names an opaque tenant tag (first 16 hex of SHA-256 of `nexal-tenant:<tenantId>`). Per-tenant caps (instances, CPU, memory, disk) are enforced when a sandbox is created. |
| Network | One docker bridge per tenant (`nexal-t-<tag>`, bridge `nx-<12 hex>`). Docker isolates bridges from each other; `nexal-storage-firewall.sh` also stops containers reaching this server, the LAN and private ranges. Mesh access is per sandbox (the member's network policy). |
| Resources | `--cpus`, `--memory` (= swap limit), `--pids-limit` per container; all of a tenant's containers in `nexal-tenants-<tag>.slice`, all tenants in `nexal-tenants.slice` (cap it so storage keeps headroom). Optional disk quota with `NEXAL_MANAGED_STORAGE_OPT=1`. |
| Definitions | Every devcontainer.json (template, inline, or the repository's own, which the connector clones itself with hooks, submodules and symlinks off) is rewritten through an allowlist: no `mounts`, `runArgs`, `privileged`, `capAdd`, `securityOpt`, `workspaceMount`, `appPort`, `initializeCommand` (it would run on this server), build `options`; Docker Compose refused; only official `ghcr.io/devcontainers/features/*` features minus the docker ones. A persistent workspace's definition is re-sanitized on every restart. |
| Labels | `nexal.tenant`, `nexal.workspace`, `nexal.managed=1` on containers, sidecars and networks: `docker ps --filter label=nexal.tenant=<tag>`. |
| Files | No host paths are mounted except the workspace's own source folder (`/var/lib/nexal/devcontainers/<workspace>/src`). The shared drive is not offered in dev containers. |

Residual risk: containers run under the rootful Docker Engine; a kernel or runtime escape is not
stopped by the above. Before opening this to members other than K, consider rootless Docker
(`NEXAL_DOCKER_SOCKET=/run/user/<uid>/docker.sock`) or `"userns-remap": "default"` in
`/etc/docker/daemon.json` (test DevPod against it first), and keep the kernel patched.

## Server prerequisites

- Linux, amd64 or arm64, systemd, cgroup v2 (`stat -fc %T /sys/fs/cgroup` prints `cgroup2fs`).
- Docker Engine (not Docker Desktop) with the systemd cgroup driver (`docker info | grep -i cgroup`
  shows `Cgroup Driver: systemd`, the default on cgroup v2).
- `git`, `iptables` (or iptables-nft), and the DevPod CLI as `/usr/local/bin/devpod`
  (https://github.com/loft-sh/devpod/releases, the `devpod-linux-<arch>` binary; it is run unmodified).
- Outbound HTTPS to the coordinator, ghcr.io / mcr.microsoft.com, and the mesh control plane.

## Steps (in order)

1. **Coordinator: migration and deploy** (from the `nexal-platform` repository root, once the
   `nexal-storage-host` branch is merged):

       npm run deploy:prod     # applies pending D1 migrations (0081_managed_hosts.sql) then deploys

2. **Build the connector for the server** (on a Mac with Go, from `nexal-connector/connector`):

       GOOS=linux GOARCH=amd64 go build -trimpath -o nexal-linux-amd64 ./cmd/nexal     # arm64 server: GOARCH=arm64
       scp nexal-linux-amd64 storage:/tmp/nexal

3. **Server: user, directories, files** (as root):

       install -m 0755 /tmp/nexal /usr/local/bin/nexal
       useradd --system --home-dir /var/lib/nexal --shell /usr/sbin/nologin nexal
       usermod -aG docker nexal
       install -d -o nexal -g nexal -m 0700 /var/lib/nexal /var/lib/nexal/secrets /etc/nexal
       install -o nexal -g nexal -m 0600 sandbox-hosting.json /etc/nexal/sandbox-hosting.json
       install -m 0644 nexal-storage.service nexal-tenants.slice /etc/systemd/system/
       install -m 0755 nexal-storage-firewall.sh /usr/local/sbin/
       systemctl daemon-reload

4. **Initialize and enroll into the operator account.** Get a one-time code (10 minutes) with the
   owner token, then enroll:

       curl -fsS -X POST -H "Authorization: Bearer $OWNER_TOKEN" https://coordinator.nexal.systems/api/enrollment
       # -> {"code":"enr_...","expiresAt":"..."}
       sudo -u nexal env HOME=/var/lib/nexal nexal init --coordinator https://coordinator.nexal.systems \
            --name "neXal storage" --config /etc/nexal/config.json
       echo "enr_..." | sudo -u nexal env HOME=/var/lib/nexal NEXAL_SECRETS_DIR=/var/lib/nexal/secrets \
            nexal enroll --config /etc/nexal/config.json
       # -> {"enrolled":true,"hostId":"<HOST_ID>",...}

   The host credential is stored in `/var/lib/nexal/secrets/host` (0600; the directory must be 0700
   and owned by `nexal`, which `NEXAL_SECRETS_DIR` enforces). If Cloudflare Access protects the
   console, the owner token is not accepted; make both calls from the Access-protected console instead.

5. **Flag it as neXal storage** (operator-only; limits are per member account on this server):

       curl -fsS -X PUT -H "Authorization: Bearer $OWNER_TOKEN" -H "content-type: application/json" \
            -d '{"maxInstances":20,"tenantMaxInstances":3,"tenantMaxCpus":8,"tenantMaxMemoryMb":16384,"tenantMaxDiskGb":120}' \
            https://coordinator.nexal.systems/api/v2/operator/managed-hosts/<HOST_ID>
       # list: GET /api/v2/operator/managed-hosts ; pause new placements: same PUT with {"state":"draining"}

6. **Firewall and start:**

       /usr/local/sbin/nexal-storage-firewall.sh        # run at every boot after docker (e.g. a oneshot unit)
       systemctl enable --now nexal-storage.service
       journalctl -u nexal-storage -f                    # expect "managed dev-container runner ready"

   Size `nexal-tenants.slice` (CPUQuota, MemoryMax) to leave the storage services their share, and
   keep `maxSandboxes` in `/etc/nexal/sandbox-hosting.json` (up to 64 with `"managed": true`) at or
   below the coordinator's `maxInstances`.

7. **Check from a member account:** `GET /api/v2/managed-runners` lists `nexal-storage` with
   `"online": true`; a subscribed member can create a dev container on it from the app.

## Environment (set in the unit)

| Variable | Default | Meaning |
|---|---|---|
| `NEXAL_MANAGED_RUNNER` | unset | `1` turns on the managed runner (Linux only). |
| `NEXAL_MANAGED_STATE_DIR` | `/var/lib/nexal` | Workspaces (`devcontainers/`), sandbox state (`sandboxes/`). |
| `NEXAL_MANAGED_HOSTING` | `/etc/nexal/sandbox-hosting.json` | Opt-in and local caps, re-read on every poll. |
| `NEXAL_SECRETS_DIR` | unset | Private credential directory (required on Linux). |
| `NEXAL_MANAGED_SLICE` | `nexal-tenants` | Parent slice; `none` drops `--cgroup-parent` (cgroupfs driver). |
| `NEXAL_MANAGED_STORAGE_OPT` | `0` | `1` adds `--storage-opt size=<disk>G` (overlay2 on xfs with pquota only). |
| `NEXAL_MANAGED_PIDS_LIMIT` | `4096` | Per dev container. |
| `NEXAL_DOCKER_SOCKET` | `/var/run/docker.sock` | Another engine socket (rootless Docker). |
| `NEXAL_DEV_MESH_IMAGE` | pinned sidecar | Mesh sidecar image override. |

## Operations

- Everything of one member: `docker ps -a --filter label=nexal.tenant=<tag>`; tag = first 16 hex of
  `printf 'nexal-tenant:%s' <tenantId> | sha256sum`.
- Tenant networks stay after their last container is deleted (harmless); remove idle ones with
  `docker network prune --filter label=nexal.managed=1`.
- Stopping the service does not stop containers; the coordinator marks the server's instances failed
  after 15 minutes without a heartbeat (`runner_unreachable`).
