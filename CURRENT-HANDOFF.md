# Nexal current engineering handoff

Checkpoint: September 19, 2026. This file is intended for a developer or LLM
resuming work without access to the original conversation. It describes an
engineering preview, not a production-approved distributed cloud.

## Product and owner decisions

- Product: Nexal Platform and Nexal Connector; entity: KWK, LLC.
- Domain: owner purchased `nexal.systems`; registration provider is not confirmed.
- Source owner: GitHub account `keithknott26`; both repositories must stay private.
- Prefer Go where practical, native Cloudflare services for the coordinator,
  and native SwiftUI/Go on Macs. MLX uses a small Python runtime adapter.
- Setup should inspect dependencies automatically, offer narrowly scoped upgrades
  with consent, and minimize setup friction. Docker is acceptable for the
  coordinator; it does not replace native Metal/MLX execution.
- Target owner hardware: base M4 Mac mini, 10 cores, 24 GB RAM, 512 GB storage;
  an M2 Mac is also intended for private and later public participation.
- Owner reported macOS 27.0, build 26A428, Swift 6.4, Node 23.6.1 and Go 1.23.1.
  Ethernet reaches a mesh/PLC network backed by DSL; do not assume data-center
  bandwidth or latency. The source build targets Node 24 and Go 1.26.
- The owner built the older Nexal native preview, opened its menu-bar app and
  enrolled the 8 GB M2. Private CPU execution was blocked by owner-activity,
  heartbeat-only startup and low measured memory, not proven successful on Mac.
- Local pooled RAM must remain private, even when idle, with no public fallback.
  Supported-app integration is desired; arbitrary-app transparent RAM is not
  an established supported implementation.

## Scope that must remain represented

- MCP access to permissioned enriched market data: stocks, options, earnings,
  PEAD research and news sentiment. Provider feeds are not live in this preview.
- Member contributions of compute, storage and properly licensed datasets.
- Private LAN pooling of approved jobs, model caches and project storage.
- Potential distributed MLX jobs with per-rank memory admission, not transparent
  operating-system RAM expansion or automatic acceleration of arbitrary apps.
- Owner-first scheduling, pause/reclaim, budgets, metering and eventual payments.
- Approved managed-cloud fallback only with credentials, budget and explicit
  authorization. Provider names do not imply implemented parity.
- Dynamic funding contribution plus usage charges minus eligible earned credits.
  More members can spread fixed costs; this is not a guaranteed falling bill.
- Strict fail-closed post-quantum requirements for the tunnel segment, not a claim
  that the entire system or every peer connection is post-quantum secured.
- Data format conversion does not itself establish redistribution permission.
  Entitlements and approved use must remain separate from membership.

## Repository map

- `nexal-platform`: Worker, D1, dashboard, services/MCP, setup, container recipe,
  product documents and financial/visual archive.
- `nexal-connector`: Go agent and private-pool package, SwiftUI source, Python MLX
  adapter and Go bridge, packaging instructions and tests.
- Both carry `CONTRACT.md` and `COMPATIBILITY` (`nexal-private-preview-api-v2`).
- Initial source checkpoint: platform `755c1c1`, connector `c34cabc`.
  These are baseline commits, not the latest documentation commits.
- Local authoritative directories are the two sibling repositories, not the old
  `kwk-platform` monorepo. Old source and local state were retained, not migrated.

## Implemented and tested boundaries

Isolated memory research increment:
[bounded TCP CPU-paging prototype](experiments/tcp-pager/README.md).
The Go transport/cache and CLI passed Linux race, integrity, cancellation and
subprocess tests; tiny guest instructions passed independent ARM emulation.
The owner subsequently reported passing `bash scripts/accept-macos.sh` on
Darwin ARM64 with Go 1.26.8: native build/signature verification and actual
HVF CPU-fault loopback paging passed. The 1 MiB logical dataset was verified
with a 64 KiB cache payload, 128 faults and 120 evictions. Exact hardware,
OS version and checkout revision were not included in that output.
See the module's VALIDATION.md and transcribed acceptance JSON.
The next gate is the same bounded workload between two separate private-LAN Macs.
Follow-up: `experiments/tcp-pager/MAC-TEST-RUNBOOK.md` now provides guided
donor/receiver scripts, repeated fresh-store acceptance and one-command Mac
memory diagnostics. The owner clarified the accepted original host is an M2
Mac mini. The owner subsequently passed the new Core Foundation probe and
observed native suite at `78bae3a` on an 8 GiB Mac, with 32 unchanged host RAM
samples. This run's exact model was not supplied; do not count it as M4
acceptance. Separate-Mac LAN testing is still pending. The final
NOT_IMPLEMENTED message is a programmed capability gate, not proof that all
possible OS integrations are impossible. Linux validation: 36 top-level/59 named Go passes,
race/vet, six lab CLI tests, four prior CLI tests, three ARM-emulation tests,
portable C sanitizers and Darwin Go cross-build passed.
`accept-memory-macos.sh` explicitly ends with exit 3 if paging passes but the
requested OS-visible RAM backend remains unimplemented. CFAllocatorCreate is
an opt-in application allocator hook, not a completed remote-memory backend;
see OS-VISIBLE-RAM-ACCEPTANCE.md. No claims of additional system RAM are permitted.
This does not change production enrollment, sharing or memory accounting and
does not establish macOS guest boot, host RAM expansion or additional VRAM.

LAN bootstrap follow-up: the M4 selected Go 1.23.1 and stopped before donor
startup. `experiments/tcp-pager/scripts/setup-lan-macos.sh --donor` now checks
prerequisites, offers consent-based installation of Homebrew `go@1.26`, and
starts the existing donor workflow. `--receiver "/path/to/client"` prepares the
receiver. `--check` and `--no-start` are available; explicit rerun commands
support restarts without boot-time services or persistent resume flags.
11 mock-toolchain tests and Bash syntax checks pass; actual M4 setup and LAN
acceptance remain pending. No reboot is requested by the script.

Native update: the owner subsequently supplied successful native build output for
platform `d601629` and connector `76345d9`: 424 TypeScript tests, Go vet/race
package tests, Swift compilation, seven XCTest cases, migrations 0001–0004
and initial app packaging. See [MAC-ACCEPTANCE.md](MAC-ACCEPTANCE.md). This
supersedes older statements that Swift compilation was wholly untested.
The owner reports the menu-bar app opened and the M2 enrolled, with real
telemetry observed. Full UI behavior, Keychain and signed distribution remain
unvalidated; latest Swift changes have not been compiled in the Linux sandbox.
The acceptance worktrees are separate from the owner's dirty staging checkout.
Docker occupies 8787; Swift local-preview initialization is fixed to that port,
so identify the container before a reviewed local service switch.

See [NEXAL-VERIFICATION.md](NEXAL-VERIFICATION.md) for evidence and commands.
Previous local testing reported 424 TypeScript tests and 190 named Go test passes
(89 top-level tests, with named subtests included in 190), race/vet, 34 Python
tests, 24 setup policy checks, 20 mocked installer scenarios, 18 subprocess
launcher tests and a local Go-to-Worker/D1 integration run with 41 checks.
Polling can change the integration check count. The fixed CPU workload completed
with synthetic Linux telemetry and no external spending. Browser QA covered six
mobile views, usage-limit persistence, error states and keyboard interaction.

Latest implemented increment:
- [Private MLX and manual acceptance](PRIVATE-MLX-AND-MANUAL-ACCEPTANCE.md):
  433 TypeScript tests passed across 12 files, root/scoped typechecks and dashboard
  build passed, all connector race/vet checks and Darwin ARM64 cross-build passed.
  Existing 34 Python tests and 62 installer/launcher policy checks passed.
  Real local Worker/D1 plus Go integration passed with manual acceptance and
  completed zero-cost private jobs (60 checks; synthetic Linux telemetry).
- Essential migration 0005 introduces short-lived private manual consent without
  falsifying owner activity. The native Accept jobs now button uses the existing
  authenticated daemon. It never bypasses memory checks. Public/paid work is
  excluded during the manual window; pause/policy/restart clear local permission.
- Enrollment confirmation displays a fixed mask without retaining the one-use
  code. New native XCTest cases are written but not executed on macOS here.
- Private-memory reservation/export and MLX placement guards are library
  safeguards, not an enabled distributed-memory runtime or OS sandbox.
- `nexal-connector/runtimes/bridge` now contains `RunLocalInference` and standalone
  `cmd/nexal-mlx-job`: fixed pinned local runtime execution, output/deadline limits,
  process-group cleanup, strict results and per-config exclusion. Main-agent
  race/vet checks and Darwin ARM64 cross-build passed. Real MLX/Metal, authoritative
  scheduler admission/reclaim, native testing and multi-Mac execution are NOT
  implemented/validated by that result. The native app does not launch MLX yet.
  See the bridge README and validation handoff before integrating it.
- [Production-hardening pass](PRODUCTION-HARDENING-PASS.md): independent reviews
  of installation, connector safety and coordinator integrity, plus main-agent
  MCP/provider deadlines, stream limits and integrated verification.
- Essential migration `0004_admission_fencing.sql` rejects missing/unconfigured
  usage authorization and uses database-clock invitation expiry. Coordinator
  lease/credential checks and budget-month calculations use execution-time SQL.
  Applied locally only; no remote migration or new spending permission.
- Connector cancels pending pulls after owner decisions, reclaims on expired
  observations independently of stalled probes, and bounds network contexts.
  Saved configuration must contain explicit, non-null `paused`; generated
  settings already do. Do not delete existing state to bypass validation.
- Installer diagnostics sanitize preload variables; real Node/npm pairing,
  compatibility checks, relative paths and bounded subprocess-group cleanup
  have regression coverage. Actual Mac acceptance is still outstanding.
- Owner-only `/api/audit` records curated state transitions atomically using
  migration `0003_audit_history.sql`. Apply pending local migrations when updating.
  No backfill, actor attribution, external tamper evidence or retention automation
  is claimed; read `AUDIT-HISTORY.md`.
- `nexal doctor` provides a sanitized local config report without credential reads,
  network calls or mutations. `--probe` explicitly enables bounded Mac telemetry.
- The owner prioritized **easy, seamless, robust installation**. Revision 2
  defaults double-click setup to native mode, adds a Start command, validates
  first-launch Xcode status, scopes toolchains, rejects ambiguous modes/remote
  Docker contexts, serializes setup and stages replacement app bundles.
  A supervised native launcher handles readiness, browser opening, port conflicts,
  deadlines and owned-child cleanup. See `INSTALLATION-BUGCHECK.md`.
- These installer tests simulate Mac tools. An actual local Wrangler/D1 launcher
  run passed on Linux, not Finder, Homebrew, Swift, Docker or macOS `open`.
- Dashboard has a separate monthly compute-usage authorization editor, with
  explicit acknowledgment and non-cash labels. Operating forecasts never grant
  spending authorization. The additive API `configured` field needs no migration.
- Connector has authenticated `policy` / `set-policy` CLI and local API controls
  for workload memory, owner reserve and idle threshold. Changes cancel work,
  persist before application and preserve pause/public-dispatch restrictions.
- Consent generation and observation sequence checks fence stale or out-of-order
  telemetry/heartbeat responses. Resume requires fresh observations.
- MCP parsing now reaches the protocol handler after authentication, preserving
  JSON-RPC errors and a streamed 16 KiB ceiling. MCP-specific version preflight
  is supported. This is still a stateless subset, not certified Claude onboarding.
- `bash scripts/verify-local.sh --integration-port 8787` provides a repeatable
  cross-repository check against an already running loopback development Worker.
  It does not install software or deploy, and does not replace Mac acceptance.

Real production tunnel dispatch, public arbitrary-code isolation, licensed live
feed delivery, payments, signed/notarized Mac releases and distributed MLX
execution remain incomplete or gated. Swift/native Keychain and the Docker
runtime require real Mac acceptance; a Darwin cross-build is not that acceptance.
Do not remove these gates to make a demo appear production-ready.

## Credentials, environments and deployment

- GitHub uploads succeeded; repositories are private and Pages is off.
- GitHub Actions is disabled pending owner review. Local tests do not prove
  GitHub-hosted CI passed. CODEOWNERS alone is not branch protection.
- No Cloudflare production deployment, DNS change or paid cloud action performed.
- Cloudflare connector authentication is currently blocked; read
  [the domain checkpoint](CLOUDFLARE-DOMAIN-STATUS.md) before retrying.
- Private hosted sandbox preview is only a development demonstration, not the
  production domain. Do not configure DNS to its ephemeral backend.
- User setup defaults to loopback. Never expose anonymous development mode.
- Do not commit credentials, live enrollment invitations, local databases,
  Keychain exports, `.env`, `.dev.vars`, tunnel tokens or raw conversation logs.
- Original inline diagrams are preserved as JSON and standalone SVG. They are
  design artifacts, not proof the proposed services have been implemented.
- Exact-domain investigation did not establish the registrant of
  `cloudfare.com`: its registrar redacts identity. Redirects, nameservers and
  registrar identity are not common-ownership evidence. See CLOUDFLARED-TRUST.md.
- The owner's Mac checkout showed dirty `cloudflare-staging`. Preserve its local
  work before any merge or branch switch; never reset, clean or overwrite it
  automatically. These repository updates alone do not update that checkout.

## Next work in order

The owner's latest requested resume point is: **finish DNS for nexal.systems
and link Claude with Cloudflare. Both remain outstanding.** Do not interpret the
successful GitHub upload as completing either task.

1. Resolve credential-type mismatch or connector failure without requesting
   secret values in chat. Inspect the zone only after successful authentication.
2. Confirm registrar, assigned Cloudflare nameservers, existing email/DNS records
   and DNSSEC status. Ask for authorization before consequential DNS changes.
3. Clone both private repositories as siblings on the M4; stop old local services
   using ports 8787/8788 before starting Nexal. Follow MAC-SETUP.md.
4. Compile/test SwiftUI and verify Keychain, setup and Docker mode on real hardware;
   fix failures and record evidence before claiming Mac acceptance.
5. Review CI permissions/costs and enable/run workflows only with owner approval.
6. Work through PRODUCTION-GATES.md before an authenticated Cloudflare pilot.
   Confirm spend limits and bindings; do not enable public jobs or payments.
7. Validate signed dispatch, actual PQ behavior, owner reclaim, pool persistence,
   data rights and cloud fallback independently before expanding the pilot.
8. Configure the requested Claude/Cloudflare connection. First confirm whether
   the owner means Claude Code developer access, a Claude app MCP connection to
   Cloudflare, or Claude consuming Nexal's own MCP endpoint. These are distinct
   integrations; none has been configured. Use least-privilege credentials and
   separate development/deployment permissions from end-user MCP access.

## Source-of-truth order

Current code, current verification and explicit release gates take precedence
over historical handoffs. Architecture and requirement files describe the target
system; financial models are illustrative assumptions rather than a business
forecast. Consult the document index for every preserved product artifact.
# Private pager bundle relay checkpoint

New opt-in commands bundle-send/bundle-receive and
experiments/tcp-pager/scripts/platform-bundle-macos.sh implement encrypted
delivery of a selected donor client folder through the coordinator to a
fresh private receiver application-support folder. No AirDrop dependency.
Requires platform migration 0006 and both Macs enrolled in the same reachable
coordinator. See experiments/tcp-pager/PLATFORM-DELIVERY.md.

Full connector race tests/vet and Darwin ARM64 cross-build pass; a cross-language
E2E test through the actual Worker handler and real local D1 passed. No owner
Mac delivery, new Keychain acceptance or remote deployment performed. User
still chooses donor bundle and compares public fingerprints; background automatic
folder collection and a dashboard transfer button are not implemented.
