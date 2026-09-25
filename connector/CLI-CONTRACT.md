# neXal connector CLI contract v1 + enrollment v2

The executable is `nexal`. Every subcommand accepts `--config /absolute/path/config.json`.
Default config is `~/Library/Application Support/Nexal/config.json` on macOS and
`~/.config/nexal/config.json` elsewhere. Output is JSON, including errors on stderr.
No credential is printed. Successful commands exit 0; failures exit nonzero.

Version 2 network enrollment is additive while legacy host enrollment migrates:

- `nexal pair-v2 --create` creates a short-lived phone enrollment. It emits a
  QR module matrix containing an HTTPS Universal Link on `link.nexal.systems`
  and an independently generated `XXXX-XXXX` manual code. The URL credential
  is in the fragment so it is not sent to the web origin or CDN logs.
- `nexal pair-v2 --status UUID` reports `waiting`, `claimed`, `authorizing`,
  `provisioning`, `joining`, `paired`, `cancelled`, `expired`, or `failed`.
- `nexal pair-v2 --cancel UUID` invalidates an uncompleted enrollment.

The polling token and completed network credential are Keychain-only. Completed
non-secret account, network and device identifiers are saved in `config.json`.
The connector never installs or pretends to supervise a privileged network
daemon; its `mesh` status remains `unavailable` until an explicitly installed
provider supplies runtime evidence.

- `nexal init --coordinator https://coordinator.example --name "My Mac"`
  Creates private-by-default, paused config and random local admin credential.
  macOS uses the login Keychain through `/usr/bin/security`; no token appears in argv.
  `--memory-limit-mib 256 --reserve-memory-mib 1024` are optional.
- `nexal enroll --code-stdin` reads the one-use enrollment code from stdin.
- `nexal coordinator-check` checks `/api/health` using the same bounded direct
  HTTPS transport as enrollment, without reading Keychain or sending credentials.
  It does not change configuration or enrollment. Success is connectivity only,
  not proof of a valid host identity or authorization. Errors distinguish DNS,
  certificate validation, timeout, connection refusal/closure, and other network
  failures using fixed messages without raw URLs, certificates or response text.
  Ambient proxy settings remain disabled and TLS verification remains required.
- `nexal run` starts a loopback-only authenticated API at `127.0.0.1:8788`,
  host heartbeat and optional pinned strict tunnel supervision. It does not install
  software, ask for root, or enable public work. While running it writes one JSON
  log record per line to stderr (attempt lifecycle, abandonment reasons, heartbeat
  failures; `INFO` and above, `DEBUG` for a development configuration). Records
  contain no credential, ciphertext, key material or coordinator URL. Stdout stays
  reserved for command output, so a supervisor must drain stderr but need not parse
  it; the record set is diagnostic and not part of the compatibility surface.
- `nexal status` queries the running local API using the admin credential. Its
  `contribution` block is the HARDENING-PLAN §36.4 / §26 answer to "why is my Mac
  or is it not contributing": one entry per condition (power, thermal, owner
  activity, free disk) with the observed value, whether that condition is
  withholding, and a plain-language reason — including when the condition is
  unknown. See the local API section for the field list and the honesty caveats.
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
  `nexal doctor --stun` is the ONE flag that makes `doctor` touch the network. It
  sends RFC 5389 STUN binding requests to two public servers
  (`stun.cloudflare.com:3478` and `turn.cloudflare.com:3478` by default, both free)
  and adds a `nat` block plus the `nat_reflexive` and `nat_mapping` checks:
  `{reachable,reflexiveAddress,mapping,summary,servers[]}` where `mapping` is
  `endpoint-independent`, `endpoint-dependent` or `unknown`. It sends no
  credential, no host identity and no configuration value, and it sets
  `networkContacted:true` so the report never claims an offline run it did not
  have. Without the flag, `nat_reflexive` reports `not_checked` and no packet is
  sent.
  READ THE SCOPE HONESTLY: this is OBSERVABILITY ONLY. A reflexive address is not
  reachability. There is no hole punching, no candidate signalling and no relay in
  this connector, so `doctor --stun` answers "would peer-to-peer over the internet
  even be feasible from this network", not "it works". `unknown` is a real answer
  and is what fewer than two answering servers yields; one sample is never a
  classification. See `TRANSPORT-NAT-DESIGN.md`.
  `doctor` also reports a `static_peers` check with the COUNT of configured static
  peers — never their addresses, labels or fingerprints, which are configuration
  values — and restates that a configured peer is authorized only while the
  coordinator lists its fingerprint.
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
  requires all three settings explicitly.
  `--min-free-disk-mib N` sets the §36.4 disk reserve (1024–1048576 MiB). It is
  OPTIONAL and 0 keeps the reviewed 10 GiB default — 0 is never "no floor".
  It is the only one of the three new conditions that is owner-tunable: free-space
  needs differ per machine, whereas "do not run on battery" and "do not run while
  thermally throttled" are §36.4 safety properties, not preferences, so no flag
  disables them.
  `--upload-mode auto|manual|unlimited` (default `auto`) and
  `--upload-limit-kib-per-second N` add the upload-throttle dimension. These two
  are OPTIONAL so an existing three-flag caller keeps working and inherits `auto`;
  silence is read as the shaped default, never as unlimited. `--upload-mode manual`
  requires a limit of 32–1048576 KiB/s; `auto` and `unlimited` require the limit to
  be absent or 0 rather than storing a number no mode consults. No bulk upload path
  exists yet (see below), so setting these changes policy, not live traffic. It persists limits atomically, cancels
  active work and waits for fresh telemetry and coordinator contact before new
  admission. It never resumes a paused host or enables production dispatch.
  Workload memory is bounded to 64–8192 MiB, owner reserve to 128–1048576 MiB,
  and idle time to 30–86400 seconds. These are admission controls for approved
  work, not an OS-enforced sandbox for arbitrary programs.
- `nexal static-peers list|add|remove` manages owner-configured peer endpoints for
  peers mDNS cannot find — principally two Macs on different VLANs.
  `nexal static-peers add --endpoint https://10.20.0.5:8443 --fingerprint <64-hex>
  [--label "studio vlan 20"]`, `nexal static-peers remove --fingerprint <64-hex>`,
  `nexal static-peers list`. Output is the stored list plus `count`.
  WHY IT EXISTS, precisely: the peer transport ALREADY permits a routed private
  address (an RFC1918 address on another subnet satisfies the same private-IP rule
  as one on this subnet), and what does not cross a VLAN is mDNS, which is
  link-local multicast. So this supplies an address; it relaxes no security rule.
  `--endpoint` must be `https` with a LITERAL private, loopback or link-local IP
  and an explicit port — the same rule `internal/pool` enforces on every dial. A
  public address is refused. A hostname is refused, because the peer transport sets
  `Proxy:nil` and performs no DNS lookup, so a name would be a setting that
  silently never works.
  `--fingerprint` is REQUIRED and is the peer's `pool.DeviceID` (64 lowercase hex).
  CONFIGURATION IS NOT AUTHORIZATION (HARDENING-PLAN §30.2): a static entry
  contributes a dial address and the fingerprint to pin, and the coordinator's
  authorized set alone decides which fingerprints may be dialed. A configured peer
  the coordinator has not authorized is displayed as "configured, not authorized"
  and is never dialed with credentials. Adding a peer here cannot widen the
  mutual-TLS allowlist.
  Entries are sorted by fingerprint, capped at 32, and duplicate fingerprints or
  endpoints are refused rather than silently replaced. `remove` takes
  `--fingerprint`, never `--endpoint`, because the fingerprint is the stable
  identity. The command edits the configuration FILE under the same exclusive lock
  `enroll` uses; it does NOT go through the running agent's local API, and its
  output says `appliesAt: next nexal run` rather than pretending a live host picked
  up the change. A config written before this feature existed still loads: the
  `staticPeers` array is optional and absent means none.
  Cross-VLAN operations also require an inter-VLAN routing rule and a firewall that
  permits the peer port between the subnets — a silent deny there looks exactly
  like a discovery failure. Alternative to static peers: enable mDNS
  reflection/repeating on the switch (UniFi, Cisco). See `TRANSPORT-NAT-DESIGN.md`.
- `nexal pair --role receiver|donor` mints a short-lived phone pairing, renders it
  as a QR code, and then polls the coordinator until it is scanned, cancelled or
  expired, printing each status transition. `--cancel <pairingId>` cancels one;
  `--status <pairingId>` reports one status and exits; `--no-poll` mints and
  renders without waiting (this is what the menu-bar app uses, so that every
  process stays inside its bounded execution); `--ascii` renders with ASCII
  characters for terminals that cannot draw Unicode half blocks; `--invert` swaps
  ink and paper for a dark terminal, where an uninverted code is a photographic
  negative most scanners refuse.
  AUTHENTICATION is the existing ENROLLED HOST credential — not the owner token
  and not a phone session — because the coordinator's pairing routes are behind
  `requireHost` and compare the device id in the path against the host's own id.
  An unenrolled Mac therefore cannot pair, and the command says so.
  OUTPUT: stdout is JSON and only JSON, ONE OBJECT PER LINE, because a poll must
  report transitions as they happen rather than printing nothing until the end.
  The mint line is `{"pairing":{pairingId,role,coordinator,expiresAt,status,
  qr:{version,mask,size,quietZone,errorLevel,encoding,moduleRows,moduleMeaning},
  claimToken}}`; each transition is `{"pairingStatus":{pairingId,status,
  expiresAt}}`; the last line of a poll is `{"pairingResult":{pairingId,status,
  scanned,…}}`; `--cancel` prints `{"pairingCancelled":true,"pairingId":…}`.
  `qr.moduleRows` is one string per row, `"1"` for a dark module, quiet zone NOT
  included — it is emitted so the native UI draws the SAME matrix the CLI drew
  rather than shipping a second QR encoder that could drift from this one.
  THE RENDERED QR GOES TO STDERR, never stdout, so the JSON stream stays
  machine-parseable while a human still sees the code.
  THE CLAIM TOKEN IS A SECRET and is NOT in the output. The `claimToken` JSON
  field holds a sentence saying so. The token authorizes a phone to claim this
  Mac; it lives in the connector's memory and inside the QR modules (which are
  the code the phone reads) and is never logged, never written to the
  configuration file and never placed in an error message.
  A pairing that nobody scans is NOT an error: the poll ends with
  `pairingResult.status = "expired"` and exit code 0, so a UI does not show a
  failure for a founder who simply did not scan in time.
  Pairing requires an https coordinator on both ends — the coordinator rejects
  non-https requests to its home routes and the phone's payload parser requires an
  https origin — so the `--dev-loopback` development profile cannot pair.
  The command takes NO configuration lock: it writes nothing, and blocking on the
  lock a running agent may hold would make pairing fail on exactly the machines
  that are working normally.
- `nexal wake --host <hostId>` asks the coordinator to wake another Mac on the
  network (`POST /api/v2/hosts/{hostId}/wake`, enrolled host credential, like
  heartbeat). Wake-on-LAN is a LAN broadcast and cannot cross a router, so the
  coordinator relays the request over the live presence stream to an AWAKE neXal
  Mac whose reported `lanKey` matches the target's, and that Mac sends the magic
  packet. The coordinator answers HTTP 202 `{requestId,relays,targetWakeForNetwork}`
  (any 2xx with a valid `requestId` and `0 <= relays <= 1000` is success). Success
  prints `{"requested":true,"requestId":"<uuid>","relays":<n>,"targetWakeForNetwork":<bool>}`
  — `requested` is the CLI's statement that the request was ROUTED, not that the
  target woke; watch `status.presence.online` for the target to appear.
  `targetWakeForNetwork:false` means the target last reported "Wake for network
  access" off, so the packet will probably not wake it.
  Failures exit 1 with `{"error":{"code":…,"message":…}}`:
  409 `no_wake_relay` -> code `no_wake_relay`, message
  "No awake neXal Mac on that computer's network can wake it.";
  429 `rate_limited` -> code `rate_limited`, message
  "Too many wake requests for that computer; try again in a minute.";
  404 `wake_target_not_found` -> code `wake_target_not_found`;
  503 (`feature_unavailable`, `events_unavailable`, `wake_gate_unavailable`) or an
  uncoded 404/501 from an older coordinator -> code `wake_unavailable`.
  Every other failure keeps code `connector_error`.
  Asking to wake this Mac's own host id is refused.
  `nexal wake --mac aa:bb:cc:dd:ee:ff` sends the magic packet FROM THIS MAC,
  immediately, without the coordinator or any credential (works unenrolled): UDP
  port 9 to the directed broadcast of every up, non-loopback, non-virtual IPv4
  interface plus `255.255.255.255`. It prints `{"sent":true,"interfaces":<n>}`,
  where `n` counts interfaces a directed broadcast went out on (0 means only the
  limited broadcast was sent). MACs must be six hex octets with `:` or `-`
  throughout; group (multicast/broadcast) and all-zero addresses are refused.
  Exactly one of `--host` / `--mac` is required. Neither form takes the
  configuration lock, so both work while `nexal run` is up. The native app maps
  `.wake(hostId:)` to `["wake","--host",id]`.
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
The policy response and PUT request have these three required integer fields:
`{memoryLimitBytes,reserveMemoryBytes,idleSeconds}`, plus the optional upload
fields `uploadMode` (string `auto`|`manual`|`unlimited`),
`uploadLimitBytesPerSecond` and `measuredUploadBytesPerSecond`, plus the optional
`minFreeDiskBytes` (§36.4 disk reserve; absent or 0 means the reviewed 10 GiB
default, and must be 1 GiB–1 TiB when set). A body carrying
only the original three fields is still accepted and decodes as `uploadMode`
`auto` with no manual limit, so a client built before this dimension existed is
not rejected. `measuredUploadBytesPerSecond` is DERIVED: it appears in the GET
response so a read-modify-write client is not refused for echoing it, and its
value is discarded on PUT — a client cannot assert a measured rate, and a policy
update preserves whatever the agent measured. PUT requires application/json;
the 4096-byte limit, numeric-loopback binding, browser-Origin rejection and
authentication apply before mutation. Missing, duplicate, unknown, null and
noninteger fields are rejected. GET accepts no body. Status includes `resourcePolicy` and `uploadThrottle`
`{mode,effectiveBytesPerSecond,source,meteredStatus,enforced,reason}`.
`enforced` is always false in this release and says so for a reason: there is NO
bulk data upload path in the connector. `internal/bundletransfer` moves 6 KB
enrollment bundles, and the Time Machine / JuiceFS storage path is gated behind
HARDENING-PLAN §21 Step 0, so the shaper, the policy and the measurement exist and
are tested but nothing large is being throttled today. `effectiveBytesPerSecond`
is 0 only for explicit `unlimited` mode; `auto` without a measurement reports the
conservative default, never 0. `meteredStatus` is `unknown` in this release because
the macOS NWPathMonitor (`isExpensive`/`isConstrained`) bridge is not implemented;
unknown applies no metered ceiling rather than guessing in either direction. A
metered path is capped hard (64 KiB/s) and never paused, and a rate cap is NOT the
per-donor monthly bandwidth budget §16 requires — that remains unimplemented.
Persistence failure pauses the live host without applying unsaved new limits.
Status also includes `contribution`
`{withholding,conditions[],summary,ownerPaused,enforced,enforcedScope,observedAt,thermalSource,synthetic}`,
where each `conditions[]` entry is `{name,value,known,withholding,reason}` for
`power`, `thermal`, `owner activity` and `free disk` (HARDENING-PLAN §36.4, surfaced
per §26). Rules a consumer may rely on:

- `withholding` is AUTOMATIC and TRANSIENT and is NEVER the same fact as `paused`.
  `paused` is the owner's deliberate, persisted choice (`pause`/`resume`); no
  condition ever writes it, and a machine cooling down never clears it. A UI must
  render them as two different statements.
- UNKNOWN NEVER WITHHOLDS. A condition that cannot be read reports
  `known:false`, `withholding:false` and a reason saying so, because an
  unreadable probe must not silently disable every host.
- `thermal` normally reads unknown on Apple silicon and that is expected, not a
  bug: `pmset -g therm` prints nothing on a cool Mac AND on a Mac that never
  reports, so silence cannot prove either answer. Thermal is therefore
  POSITIVE-ONLY — a stated `CPU_Speed_Limit` below 100 or a nonzero thermal /
  performance warning level is believed; everything else is unknown. The
  authoritative source is Swift's `ProcessInfo.processInfo.thermalState`, which is
  not bridged yet, so thermal protection is currently INERT on that hardware and
  says so in its reason string.
- `power` treats a desktop (an `AC Power` line with no internal battery) as a
  fully known healthy state; that condition can never withhold on a Mac mini.
- `enforced` is `true`, unlike `uploadThrottle.enforced`, and `enforcedScope`
  states the limit precisely: withholding gates local job admission and sets the
  host heartbeat's "do not send me work" flag, so a withholding host stops being
  offered attempts. It does NOT gate bulk storage/upload traffic, because no such
  path exists in the connector (§21 Step 0). The heartbeat carries no "why", so the
  reason is visible locally only.
- `synthetic` is true under `run --dev-assume-idle`, which substitutes a
  development fixture (AC power, ample disk, thermal still unknown) exactly as it
  already substitutes idle/memory telemetry.

Status also includes two ADDITIVE objects for live presence and Wake-on-LAN.
Both are always present and their arrays are never `null`:

- `presence` `{connected,online[],updatedAt,detail}`. `online` is the sorted list
  of host ids the coordinator's live stream (`GET /api/v2/hosts/events`,
  WebSocket, host credential) last reported online. `connected` is true while the
  stream is open and has delivered its first snapshot. The set is RETAINED while
  disconnected, so a network blip does not make every Mac look offline — read
  `connected` before trusting `online`, and `updatedAt` (RFC 3339 UTC, `""` when
  nothing has arrived) says how old it is. `detail` is a short human reason when
  not connected (`"cannot reach the coordinator"`, `"this coordinator does not
  offer live presence yet"`, `"this Mac was removed from the network"`, `"live
  presence is not running"`, …) and `""` while connected. The text is for display,
  not a stable code. The online set is display and routing input only; it
  authorizes nothing. Reconnects use jittered exponential backoff (1–60 s); an
  older coordinator's 404 is retried every 10 minutes; after removal (event
  `host.removed` for this host or close code 4001) the stream stays down until
  `nexal run` restarts. Close code 4002 ("superseded by a newer connection from
  this host") is NOT removal: the agent waits the normal backoff and reconnects.
- `wake` `{macs[],wakeForNetwork,reported}`. `macs` are this Mac's physical
  Ethernet/Wi-Fi addresses (virtual interfaces and locally-administered — e.g.
  private Wi-Fi — addresses excluded). `wakeForNetwork` is `enabled`, `disabled`
  or `unknown`, read from the `womp` line of `pmset -g` (macOS System Settings >
  Energy > "Wake for network access"); unknown off macOS or when pmset cannot be
  read. `reported` is true only when the coordinator accepted the CURRENT facts
  via `PUT /api/v2/hosts/wake-info` `{macs,lanKey,wakeForNetwork}`; it is false
  before the first report, after a failure, when facts changed, and against a
  coordinator that predates the route (404/503, which is not logged as a fault).
  Facts are re-read at start and every 5 minutes and sent only when they change.
  `lanKey` (not shown in status) is `hex(sha256(sorted, deduplicated IPv4 network
  prefixes of those interfaces, comma-joined))` — prefixes ONLY. It is not unique
  on its own (many homes are 192.168.1.0/24); the coordinator salts it with the
  source IP it observes on the PUT. See `internal/wol/facts.go`.
  While relaying, a connected agent sends magic packets for `wake.request` events
  that target another host (bounded to 8 MACs, de-duplicated, at most one per
  second) and logs each result.

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
