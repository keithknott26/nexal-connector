# internal/sandbox: throwaway hosts on this Mac

Runs sandboxes for the coordinator: full Linux VMs (Virtualization.framework via a
separate `nexal-vmhost` launchd job per VM) and dev containers (an unmodified
`devpod` binary). Standard library only. Behaviour is specified in
`docs/THROWAWAY-HOSTS.md` and the "v2 CONTRACT" in `docs/sandboxes/API.md`.

## Owner opt-in: `sandbox-hosting.json`

The Mac app writes, the connector only reads (re-read on every poll, no restart needed):

    ~/Library/Application Support/Nexal/sandbox-hosting.json
    {"enabled": true, "maxSandboxes": 5, "placement": "members"}

- `placement` also accepts the older spellings `any` (= `members`) and `mine` (= `owner`); anything else is refused.
- `enabled` (bool): the opt-in. Missing file, or an invalid file, means **not opted in**.
- `maxSandboxes` (1..10, default 5), `placement` (`members` | `owner`, default `members`).
- Unknown fields are ignored. Write `enabled:false` to opt out; do not delete the file
  (a deleted file is not published to the coordinator).
- The connector applies it as admission caps and publishes it with
  `PUT /api/v2/hosts/:id/sandbox-hosting` whenever it changes. Disabling blocks new
  placements only; running sandboxes continue until deleted or expired.
- Dev containers need Colima, Lima, Podman or OrbStack plus the `docker` and `devpod`
  command-line tools. Docker Desktop is refused (`docker_desktop_only`). The mesh
  sidecar image defaults to `DefaultMeshImage` (`ghcr.io/keithknott26/nexal-mesh-sidecar:<version>`,
  built from `connector/sidecar`; the package must be public). Set `NEXAL_DEV_MESH_IMAGE`
  in the connector's environment to override it (local build, mirror, newer version).
  Check an image with `macos/scripts/verify-sidecar.sh [image]`.

## Lifecycles

| | persistent | ephemeral |
|---|---|---|
| Mac/VM restart or stop | disk and identity kept (hostname, SSH host key); VM restarted from its disk | wiped; re-created from the image after a fresh key arrives |
| Expiry | none | `expiresAt`, enforced locally in wall-clock time |
| Shared drive | `rw` by default | `ro` by default |
| Delete | wipes disk/workspace, mesh sidecar, secrets | same |

New task payload fields handled: `sandboxKind` (`vm`|`devcontainer`; inferred from
`devcontainer` when absent), `lifecycle` / `persistent`, `devcontainer`, `sshCaPublicKey`,
`driveMode`, `driveToken`, plus task kinds `vnc-password` and `rejoin`.

## Sleep and wake (no keep-awake assertion)

Sandboxes sleep with the Mac. `Manager.RunPower` (started by the agent) detects wake from
the wall-vs-monotonic clock gap, and sleep from the lid state (`ioreg`
`AppleClamshellState`). It reports `POST .../sandbox-hosting-state {awake,onBattery}`
(retrying until accepted), pauses/resumes records, and re-polls at once after a wake.
After a sleep of 8 minutes or more (or when the guest reports its mesh down) the runner
sends `needsKey:true` in its state report; the coordinator answers with a `rejoin` task
carrying a fresh one-use key. Running sandboxes are re-reported every 30 s.
Limitation: a sleep with no lid event (menu, idle) is only noticed on wake.

## Guest channel (after first boot)

A unix socket per VM (`<id>.guest.sock`, in a short directory because of the macOS 104-byte
socket path limit) that `nexal-vmhost` bridges to a second virtio console port
(`/dev/hvc1` in the guest). One JSON line per request and per reply:

    {"op":"vnc-password","password":"..."}   -> guest runs `nexal-vnc-password set <pw>`
    {"op":"mesh-rejoin","key":"..."}         -> mesh runtime re-registers with the key
    {"op":"mesh-status"}                     -> {"ok":true,"connected":true,"meshIp":"100.x"}
    reply: {"ok":true,...} | {"ok":false,"error":"..."}

The guest agent must execute only these three operations. The first-boot console report
(`NEXAL-FIRSTBOOT {...}`) is unchanged.

## Dev containers

`devpod.go` wraps the unmodified `devpod` binary (`devpod up <source> --provider docker`,
private `DEVPOD_HOME`), joins the mesh with a sidecar container that shares the dev
container's network namespace, and tears down container, volumes, sidecar, DevPod state
and secrets. It sits behind the `DevOps` interface; tests use fakes. Error codes:
`no_container_runtime`, `docker_desktop_only`, `devpod_missing`, `devpod_failed`,
`mesh_sidecar_failed`, `ssh_setup_failed`, `mesh_image_unconfigured`, `invalid_devcontainer`.

- Default image: a dev container with no repository, template or devcontainer.json gets
  template `devbox` = `DefaultDevboxImage` (`ghcr.io/keithknott26/nexal-devbox:<version>`,
  built from `connector/devbox` by `.github/workflows/devbox-image.yml` on a `devbox-v*`
  tag; the package must be public). Debian slim + git, build tools, Go, Python 3, Node LTS
  and openssh-server (so the SSH setup needs no apt-get at start).
- Resources: containers use their own (small) sizes from the coordinator, default
  1 CPU / 512 MB (`devDefaultSize`), and admission counts those numbers. The dev
  container gets `--cpus`, `--memory` (= `--memory-swap`, no extra swap) and
  `--pids-limit` as runArgs for template/inline sources, and the same limits via
  `docker update` after `devpod up` for every source (repos bring their own
  devcontainer.json). Disk is not enforced per container (`--storage-opt size=` needs
  overlay2 on XFS with project quotas, which Colima/Lima/OrbStack do not use): the size's
  disk figure is accounting only. The mesh sidecar is capped at 0.5 CPU / 128 MB / 256 pids.
- Delete removes the sidecar, the workspace container(s) with their anonymous volumes
  (`rm -f -v`), `devpod delete`, the sidecar identity volume, images DevPod built for
  the workspace (`devpod-*`/`vsc-*`, best effort; shared pulled images such as the devbox
  stay as a cache) and the state directory (DevPod home, generated source, secrets).
  An ephemeral container found gone after a restart is deleted the same way and re-created.
- Join secrets: the one-use setup key, hostname, lifecycle and `NEXAL_MESH_URL` (the VM
  seed's name; the sidecar also accepts `NEXAL_MANAGEMENT_URL`) go in a 0600 file in
  `<state>/<workspace>/boot/`, bind-mounted at `/run/nexal-boot` - not `--env-file`/`-e`,
  so `docker inspect` shows only the mount. The connector deletes the directory as soon
  as the sidecar reports `NEXAL-FIRSTBOOT` (or fails). The management URL comes from the
  task's `mesh.managementUrl`; a dev task without a valid https one is refused
  (`invalid_devcontainer`) instead of letting NetBird fall back to its public server.
- SSH: the sidecar runs no sshd. After the join the connector `docker exec`s
  `DevSSHDScript` as root in the dev container: it installs openssh-server if missing
  (apt/apk/dnf/microdnf/yum/zypper/pacman), adds login `nexal` as an alias of the
  workspace's remote user (from the `devcontainer.metadata` label; else the uid-1000 user;
  else root), and starts an sshd on `<meshIp>:22` with the network CA as
  `TrustedUserCAKeys` (principal `nexal`, no passwords, no authorized_keys). Its host key
  (`/var/lib/nexal-ssh`, kept by persistent workspaces) is reported as
  `hostKey`/`hostKeyFingerprint`, as for a VM. Without `sshCaPublicKey` no sshd is started.
  The sshd is not supervised: if the dev container restarts outside `Up`, SSH returns
  with the next `Up` (reconcile/rejoin).
- Shared drive: not available in dev containers (the VM's FUSE helper is not in dev
  images, and FUSE would need `/dev/fuse` + `CAP_SYS_ADMIN` on the dev container itself).
  The drive token is not passed to either container. `DevUpResult.DriveUnavailable` and
  `driveUnavailable` in `state.json` say so, and the connector logs it. It is not in the
  coordinator state report yet: `POST sandbox-state` rejects unknown keys, so the
  coordinator must accept `driveUnavailable` first (TODO in `bootDev`).
- Sidecar image: `connector/sidecar` (Dockerfile + entrypoint), released as
  `ghcr.io/keithknott26/nexal-mesh-sidecar:<version>` by `.github/workflows/sidecar-image.yml`
  on a `sidecar-v<version>` tag; `DefaultMeshImage` must name the same version. Base images
  are not digest-pinned yet (TODO in the Dockerfile; the workflow prints the digests).

## Files shared with the Mac app (`~/Library/Application Support/Nexal/sandboxes/`)

- `state.json` (written on every state change): an array of
  `{id,name,state,kind,lifecycle,paused,meshIp,expiresAt,persistent,startedAt,...}`. It is also the
  runner's own record file (extra fields are internal), so the app must ignore unknown keys.
- `kill-requests/<sandboxId>`: the app creates an empty file; the runner polls every 2 s, calls
  `Manager.Kill(id)` and deletes it. Busy sandboxes keep the request for up to 10 minutes; unknown ids and
  non-regular files are dropped.

## Wire notes

- State reports use `ackTaskId` (vnc-password and rejoin acks), `needsKey` and `hostKey`
  (`ssh-ed25519 AAAA...`, from the guest's first-boot line; the fingerprint is verified against it).
- First-boot env: the seed renders `NEXAL_MESH_URL` (not `NEXAL_MANAGEMENT_URL`) and optional
  `NEXAL_DRIVE_URL`; `seed_firstboot_test.go` parses `nexal-first-boot` to keep both in sync.
- `nexal sandbox --action list|connect --id <id> --kind ssh|vnc|files [--public-key-stdin]` (see CLI-CONTRACT.md).

## Base images

A task's image carries `url` plus a hash: `digest` (`sha256:<64 hex>` or
`sha512:<128 hex>`) or the legacy `sha256` field (64 hex). `digest` wins when
both are present. The download is verified with the matching algorithm
(constant-time compare) and cached under the digest (`<sha256hex>` or
`sha512-<hex>`); a mismatch is refused and nothing is kept.

The hypervisor needs a raw disk. `.raw` images are used as downloaded.
qcow2 (including compressed Ubuntu cloud images) is converted by the built-in
pure-Go converter (`QCOW2Converter`: v2/v3, deflate clusters, sparse output;
no backing files, encryption, external data files, zstd, or more than 64 GiB).
Per-sandbox disks are grown to the requested size after the APFS clone.
