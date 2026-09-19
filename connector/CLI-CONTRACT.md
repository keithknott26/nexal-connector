# Nexal connector CLI contract v1

The executable is `nexal`. Every subcommand accepts `--config /absolute/path/config.json`.
Default config is `~/Library/Application Support/Nexal/config.json` on macOS and
`~/.config/nexal/config.json` elsewhere. Output is JSON, including errors on stderr.
No credential is printed. Successful commands exit 0; failures exit nonzero.

- `nexal init --coordinator https://coordinator.example --name "My Mac"`
  Creates private-by-default, paused config and random local admin credential.
  macOS uses the login Keychain through `/usr/bin/security`; no token appears in argv.
  `--memory-limit-mib 256 --reserve-memory-mib 1024` are optional.
- `nexal enroll --code-stdin` reads the one-use enrollment code from stdin.
- `nexal run` starts a loopback-only authenticated API at `127.0.0.1:8788`,
  host heartbeat and optional pinned strict tunnel supervision. It does not install
  software, ask for root, or enable public work.
- `nexal status` queries the running local API using the admin credential.
- `nexal doctor` emits a versioned, read-only JSON setup report without requiring
  the agent to be running. It reads only the nonsecret configuration file and
  omits host names/IDs, endpoints, paths and raw errors. It never reads Keychain,
  admin/host credentials or tunnel token files, changes settings, installs
  software, starts workloads or makes network requests.
  `nexal doctor --probe` explicitly opts into the three bounded local macOS
  telemetry commands and reports point-in-time memory/idle checks, not execution
  authorization. Default output marks telemetry, connectivity and credentials
  as not checked. A missing/invalid configuration produces a blocked check in
  the report, not a raw filesystem error. Exit 0 means a report was produced,
  NOT that all checks passed; inspect `checks[].status`. Invalid flags or an
  output failure exit nonzero. `productionReady` is always false in this release.
- `nexal pause` persists paused state and cancels active work; requires the running API.
- `nexal resume` persists resumed state; requires the running API.
- `nexal accept-jobs` asks the running development connector to accept explicitly
  private, zero-cost jobs for ten minutes even while the owner is active.
  It enables development pull in that process without starting another daemon.
  The existing memory and lease checks remain; no telemetry is fabricated.
  Repeated clicks do not extend an active grant. Pause/cancel, policy change
  or restart clears it. Production rejects the command.
  Requires matching preview v2 coordinator and migration 0005.
- `nexal policy` reads the running agent's effective resource limits.
- `nexal set-policy --memory-limit-mib 128 --reserve-memory-mib 512 --idle-seconds 600`
  requires all three settings explicitly. It persists limits atomically, cancels
  active work and waits for fresh telemetry and coordinator contact before new
  admission. It never resumes a paused host or enables production dispatch.
  Workload memory is bounded to 64–8192 MiB, owner reserve to 128–1048576 MiB,
  and idle time to 30–86400 seconds. These are admission controls for approved
  work, not an OS-enforced sandbox for arbitrary programs.
- `nexal tunnel-check` verifies the configured binary digest/configuration and reports
  policy evidence, NOT live PQ attestation.
- `nexal self-test --samples 1000000` runs the fixed CPU workload offline with a
  five-second deadline, no coordinator, no grants, no metering and no public
  execution. It is usable in production on an actual Mac with known reserved
  memory headroom. Stop `run` first; the same exclusive process lock prevents
  concurrent self-test and leased execution. This is an explicit owner request,
  so owner activity and dispatch pause do not block the offline test.

Development-only: `init --coordinator http://127.0.0.1:8787 --dev-loopback
--dev-secrets` explicitly enables nonproduction 0600 file credentials. Both flags
are required. `run --dev-private-pull` enables the private CPU prototype only in
that development config. `run --dev-assume-idle` substitutes known synthetic
telemetry for development tests; status marks it as synthetic. No production
marketplace or incoming tunnel job dispatch is enabled in this release.
For non-Mac end-to-end tests use `enroll --code-stdin --dev-synthetic-hardware`
to register an explicitly simulated 8 GiB inventory matching synthetic run
telemetry. Without it, unknown physical memory is truthfully registered as zero
and the coordinator correctly rejects positive available-memory heartbeats.

`--listen` on init may change the port, but only numeric loopback addresses are
accepted. No tokens or enrollment codes in command arguments or environment.
Swift should invoke the CLI with separate argument strings, not through a shell.
The native UI can consume `status` JSON and invoke `pause` / `resume`; it must
not duplicate credential storage or implement a second resource scheduler.

Local API: authenticated `GET /v1/status`, `POST /v1/pause`,
`POST /v1/resume`, `POST /v1/accept-jobs`, `POST /v1/cancel`,
`GET /v1/policy` and `PUT /v1/policy`.
Use `Authorization: Bearer <admin secret>`.
The policy response and PUT request have exactly these three integer fields:
`{memoryLimitBytes,reserveMemoryBytes,idleSeconds}`. PUT requires application/json;
the 4096-byte limit, numeric-loopback binding, browser-Origin rejection and
authentication apply before mutation. Missing, duplicate, unknown, null and
noninteger fields are rejected. GET accepts no body. Status includes `resourcePolicy`.
Persistence failure pauses the live host without applying unsaved new limits.
Status additionally reports `manualAcceptanceSupported`, `ownerActivityOverride`,
optional `acceptJobsUntil`, and optional `executionBlocker`. An absent blocker is
not a promise of queued work. `/v1/accept-jobs` accepts no caller-selected duration,
workload or resource bypass. Its authenticated local owner decision is temporary.
Consent changes invalidate in-flight observations; old and out-of-order telemetry
or heartbeat responses cannot restore execution eligibility. Pausing/resuming
also requires fresh observations, and stale memory observations are not advertised.
The CLI/API policy controls are implemented; a native SwiftUI policy form is not.
Local job-dispatch endpoints deliberately fail closed until Access JWT + signed
grant + verified tunnel dispatch integration is implemented and reviewed.
# Private pager bundle relay

Opt-in CLI additions (not native menu controls):

- `bundle-receive --config ABSOLUTE_PATH --from-host HOST_ID [--path-only]`
- `bundle-send --config ABSOLUTE_PATH --transfer ID --receiver-key-sha256 PUBLIC_HASH --bundle ABSOLUTE_CLIENT_FOLDER`

Receiver generates an ephemeral ML-KEM-768 key, prints public pairing metadata
to stderr and waits up to ten minutes. Sender reads only a private four-file
pager client folder, validates it and uploads ciphertext for that named receiver.
Receiver writes a fresh private application-support folder, acknowledges the
relay and outputs a JSON receipt or path only. Neither command starts jobs,
changes enrollment, pauses the running agent or requires its exclusive config lock.
Keychain authorization may prompt on macOS. No bearer tokens or private keys
are accepted in command arguments. Full platform protocol, prerequisites and
limits: `experiments/tcp-pager/PLATFORM-DELIVERY.md` in the repository.
