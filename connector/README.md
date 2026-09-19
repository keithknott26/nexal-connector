# Nexal Mac connector

An owner-first Go connector and **development private-pull prototype**, not a
production marketplace worker or a certified post-quantum system. It runs one
fixed, cancellable Monte Carlo CPU workload. It never evaluates shell commands,
Python, URLs, uploaded programs, model downloads, or job-supplied environment.

## Build and test

The unified module requires **Go 1.26**, standard library only. This minimum was
raised from the original core-only Go 1.23 target because private-pool storage
uses newer secure `os.Root` filesystem APIs.

```sh
go build -trimpath -o build/nexal ./cmd/nexal
go test -race ./...
go vet ./...
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -o build/nexal-darwin-arm64 ./cmd/nexal
```

The arm64 binary is cross-compiled, **not signed, notarized or run on a Mac** in
this sandbox. No cloudflared client is bundled or automatically installed.
No launch daemon, Login Item, privileged service, root prompt, firewall change,
paid provider fallback, deployment or boot persistence is installed.

## CLI

The stable machine-readable interface is in [CLI-CONTRACT.md](CLI-CONTRACT.md).
Each command emits JSON; errors go to stderr without raw response bodies,
credentials, enrollment codes or diagnostic logs.

```sh
nexal init --coordinator https://coordinator.example --name "Owner Mac"
# Supply the one-use invitation on stdin, not in arguments:
nexal enroll --code-stdin
nexal run
# In another terminal:
nexal status
nexal resume
nexal pause
```

Initialization is paused and private by default. The config must be in its own
0700 directory. It is atomically replaced with mode 0600 and never contains the
host or local API tokens. macOS uses `/usr/bin/security` and the login Keychain.
Writes use the security utility's stdin interpreter, not password-bearing
arguments, shell interpolation or environment variables; output is suppressed
and every write is read back for verification. Keychain ACL prompts/failure
behavior still require real-Mac acceptance testing. A copied binary should not
be presented as signed/notarized or an audited Keychain client.

`status`, `pause`, `resume` and `cancel` require the running loopback API.
Pause stops active computation immediately, persists local policy, and wakes the
host heartbeat. Remote owner-side host pause/approval controls are independent.
Host token renewal/rotation is not implemented; use an owner-authorized new
invitation/new configuration and revoke the old identity when required.

### Offline owner self-test on a real Mac

```sh
# Stop `nexal run` first; both commands share the same exclusive process lock.
nexal self-test --samples 1000000
```

This is useful without enrollment: the fixed CPU computation is local, bounded
to one million samples and five seconds, and has no network or ledger effects.
Production requires known memory headroom. Since the owner explicitly requests
it, owner activity/dispatch pause do not block this offline diagnostic. It is
not a way to authorize remote production jobs.

### Explicit development end-to-end path

Use an isolated local coordinator with fake/non-cash development state:

```sh
nexal init --config /absolute/private-demo/config.json \
  --coordinator http://127.0.0.1:8787 --name "DEVELOPMENT PREVIEW" \
  --dev-loopback --dev-secrets
nexal enroll --config /absolute/private-demo/config.json \
  --code-stdin --dev-synthetic-hardware
nexal run --config /absolute/private-demo/config.json \
  --dev-private-pull --dev-assume-idle
# Another terminal:
nexal resume --config /absolute/private-demo/config.json
nexal status --config /absolute/private-demo/config.json
```

The synthetic enrollment flag deliberately registers an **8 GiB simulated
inventory**; synthetic run telemetry advertises **4 GiB simulated available
memory** and is marked `synthetic: true`. Both are forbidden for production
configuration. Without the enrollment flag, non-Mac hardware memory is unknown
and registered as zero; positive synthetic heartbeats then fail the server's
inventory bound. Do not describe simulation as measured hardware.

Only numeric loopback permits development HTTP. Production coordinator traffic
requires HTTPS with certificate validation. Redirects and ambient HTTP proxy
settings are disabled so host bearer credentials cannot move to another origin.
Development credentials are separate 0600 files, permitted only with explicit
nonproduction flags.

Submit the approved private job through the owner-authorized coordinator API
with a unique `Idempotency-Key`, `execution: "private"` and the enrolled
`targetHostId`. Coordinator approval/paused policy may also need owner updates.
The CLI does not ask for or retain a general owner API token.

**Outbound pull is not protected by the incoming cloudflared tunnel.** Production
remote execution, including marketplace work, remains blocked even if the
tunnel connects or a diagnostic reports QUIC/PQ.

## Execution and local API safety

- The built-in template is `monte-carlo-pi-v1`, streaming constant-memory samples
  on a single CPU worker. Resource estimates include a 64 MiB process-headroom
  floor; owner memory reserve and approved allocation are checked before work.
  This is application admission, not a hard OS sandbox/RSS enforcement claim.
- macOS uses bounded, timeout-limited `/usr/sbin/ioreg`,
  `/usr/bin/vm_stat` and `/usr/sbin/sysctl`. Idle parsing accepts exactly one
  numeric `HIDIdleTime`. Memory counts only free plus speculative pages, not
  active/compressed memory or swap. Unknown/stale telemetry fails closed.
- One active attempt and one connector process per config. Pausing, cancellation,
  owner activity, resource loss, heartbeat rejection or lease expiry stop work.
  Heartbeat I/O does not block the owner monitor.
- Every offer validates host identity, identifier syntax, template, sample count,
  nonnegative cost bound and a fresh lease of at most two minutes. The coordinator
  contract normally supplies a 60-second lease renewed every 15 seconds.
- The workload itself checks cancellation and the effective deadline every
  4,096 samples. A separate 50 ms watcher fences a stalled lease renewal. No
  stale result is submitted intentionally; completion HTTP requests also have
  the current lease deadline.
- A private atomic attempt journal rejects changed replays, deduplicates attempts
  across process restarts, and fences interrupted work rather than rerunning it.
  Identical completion retries never rerun the workload. At 10,000 journal
  entries the agent stops admission rather than evicting replay protection.
  Unconfirmed completion remains unconfirmed; the coordinator is settlement
  authority. A production journal compaction/reconciliation protocol is deferred.
- Local API accepts only numeric loopback peers and Host values, disallows
  browser Origin headers, uses a random 256-bit admin bearer and constant-time
  hashed comparison, caps bodies at 4 KiB/headers at 8 KiB and concurrent handlers
  at 16, and sets read/write/idle deadlines. There is no permissive CORS.
- Incoming `/v1/attempts*` and `/v1/jobs*` **always reject**. An admin token or
  `Cf-Access-Jwt-Assertion` header is not an execution grant. Independent Access
  JWT signature/issuer/audience checks, signed job grants and replay bindings
  are not integrated; accepting any grant would be unsafe.
- Journal data and credentials are not available to the fixed computation as
  job inputs. However, this is one owner-trusted process, not proven hostile-code
  isolation. Host administrators can inspect their own process and plaintext.

Private pool mechanisms live in `internal/pool` under separate ownership.
Their registry, storage and MLX planner are **not yet wired into this daemon's
single admission authority or native UI**. Do not run a second independently
overcommitting scheduler or claim distributed execution from planning alone.

## Strict cloudflared policy

An operator must provision and independently verify an official Cloudflare
binary and per-host tunnel token. There is no `--skip-verification`, `--token`,
arbitrary argument, automatic install, transport fallback or root mode.

Add a `tunnel` object to the private config after stopping the agent:

```json
{
  "binary": "/absolute/operator-managed/cloudflared",
  "sha256": "<64 hex characters: independently checked installed binary digest>",
  "version": "<exact YYYY.M.P release>",
  "architecture": "arm64",
  "sourceUrl": "https://github.com/cloudflare/cloudflared/releases/download/<version>/<official-artifact>",
  "verificationMethod": "<publisher evidence/checksum method; explicitly record any missing evidence>",
  "tokenFile": "/absolute/private-directory/tunnel.token",
  "hostname": "host-identity.your-owned-domain.example"
}
```

This is a schema example, not usable release pin/evidence. `sha256` is the
**installed executable's** digest; if the official download is an archive, record
archive verification and extraction separately. A locally calculated digest and
operator-supplied source URL do not prove publisher identity.

`nexal tunnel-check` validates absolute paths, architecture/version metadata,
official GitHub release URL shape, binary integrity/non-writability, a bounded
0600 token file, and the binary's exact reported version. `run` checks again
immediately before process launch and writes a private effective config with
one loopback ingress and a terminal 404 catch-all. Launch arguments are fixed:

```text
tunnel --config <private-effective-config> --no-autoupdate
  --protocol quic --logformat json run --post-quantum --token-file <private-file>
```

No token appears on the command line. The environment is rebuilt from a minimal
allowlist; `TUNNEL_*`, `QUIC_*`, proxy, loader and inherited config overrides do
not reach cloudflared. Auto-update is disabled; operators must stage/re-pin
security updates rather than leave vulnerable releases frozen.

Sanitized bounded JSON diagnostics record connection/protocol and recognized
hybrid group names when supplied. QUIC alone never becomes PQ verification.
Classical protocol/group or disabled-PQ evidence quarantines the process.
Oversized diagnostic lines terminate it. Process exit/failure terminates the
supervised run without automatic restart, weaker transport or alternate route.

`tunnel-evidence.json` records policy, pin metadata, observation time and an
explicit verification gap. **`verified` and `attestation` remain false.**
There has been no live Mac tunnel, PQ negotiation test, signature/notarization
validation, replica inventory audit, dashboard-side route/Access audit,
UDP-blocked negative test or mixed-replica downgrade acceptance test here.
Remote-managed route configuration is not attested by the local ingress file.

The precisely scoped intended protection is **strict post-quantum key agreement:
Mac-to-Cloudflare tunnel**. It does not cover the outbound pull, browser/client
traffic, storage, loopback, LAN/MLX interconnect, job signatures or host memory.
Cloudflare remains a trusted intermediary.

Policy references: [Cloudflare run parameters](https://developers.cloudflare.com/tunnel/reference/run-parameters/),
[official downloads](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/),
[PQ segment boundaries](https://developers.cloudflare.com/ssl/post-quantum-cryptography/),
and [origin Access JWT validation](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/).
The implementation follows [the repository contract](../CONTRACT.md) and
[architecture v0.6](../../nexal-coordinator-tunnel-mac-architecture.md), with the
production gates above intentionally unresolved rather than falsely enabled.

## Tested here versus still required

Core tests cover unsafe URLs, TLS trust, redirects/credential boundaries,
secret/config permissions, process exclusion, exact REST payloads, unknown
templates, invalid grants, resource admission, stale leases, cancellation,
idempotent/crash replay, local API request/auth bounds, strict tunnel flags,
digest/version checks and downgrade/quarantine evidence.

`cmd/nexal` also tests the complete local mock REST enrollment → heartbeat →
private pull → fixed computation → completion path plus authenticated owner
pause/resume. Mock tunnel scripts verify pin/version behavior, not real
cloudflared functionality or publisher provenance.

Required before production: real Mac Keychain/idle/memory validation; signed
native packaging; independent pinned cloudflared supply-chain and live PQ
evidence; all-replica/route verification; Access + signed-grant dispatch;
cross-scheduler shared admission; token lifecycle/reconciliation; and operational
failure tests. No public jobs, earnings, payout capability, live model execution
or production readiness should be inferred from the successful CPU prototype.
