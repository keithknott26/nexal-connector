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
- Dev containers also need `NEXAL_DEV_MESH_IMAGE` (the mesh sidecar image) in the
  connector's environment, and Colima, Lima, Podman or OrbStack plus the `docker` and
  `devpod` command-line tools. Docker Desktop is refused (`docker_desktop_only`).

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
`mesh_sidecar_failed`, `mesh_image_unconfigured`, `invalid_devcontainer`.

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
