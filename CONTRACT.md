# Nexal implementation contract v1

This repository is an engineering preview, not a production-approved service.
Canonical scope lives in the platform repository's docs/scope/architecture-v0.6.md
and docs/scope/requirements-v5.md. Current code and release gates take precedence.

## Layout and ownership
- apps/coordinator: Cloudflare Worker, D1 database, R2 objects. Coordinator team owns.
- apps/dashboard: React/Vite dashboard, same-origin production API; preview VITE_API_BASE optional.
- packages/services: framework-free TypeScript MCP/data/provider/pricing functions and tests. Integration team owns.
- connector: Go module `nexal/connector`; stdlib first. Core team owns cmd/nexal and internal/agent, internal/tunnel, internal/client, internal/config.
- connector/internal/pool: private pool storage, admission, cluster planner; pool team owns.
- macos: SwiftUI native shell sources/build instructions. Runtime team owns.
- runtimes: Python approved MLX runtime, manifests, smoke tests. Runtime team owns.
- docs, scripts, root package.json, CI, integration tests: main agent owns.

## HTTP v1 (all JSON)
Dates are ISO8601 UTC strings. Durations seconds. Money integer USD cents. Memory/storage bytes.
Error shape {error:{code,message},requestId?}. No stack traces/secrets.
Bearer authentication for owner API in pilot; production token must be long random,
supplied through a server-side secret binding and never embedded in frontend source.
Host tokens and enrollment codes are hashed in D1; the owner secret is supplied by
the runtime, not stored in D1. Dev preview is isolated with synthetic fixtures and zero external spending.
No unrestricted CORS in production. Explicit DEVELOPMENT environment for preview only.

GET /api/health -> {status:"ok",mode:"development"|"production",version:"0.1.0"}
GET /api/overview -> {hosts:Host[],jobs:Job[],datasets:Dataset[],ledger:LedgerEntry[],budget:Budget,usageBudget:object,capabilities:object,listLimits:object}
POST /api/enrollment -> {code,expiresAt}; owner authorized, one-use, ten-minute enrollment invitation.
POST /api/hosts/enroll {code,name,platform,arch,cpuCores,memoryBytes,storageBytes} -> {hostId,token}
POST /api/hosts/:id/heartbeat (host Bearer) {ownerActive:boolean,availableMemoryBytes:number,pq:{configured:boolean,verified:boolean,protocol:string},version:string} -> {ok:true,leaseSeconds:60}
PATCH /api/hosts/:id (owner) {marketplaceEnabled?:boolean,paused?:boolean,approved?:boolean}
POST /api/hosts/:id/revoke (owner) -> {ok:true}
GET /api/hosts/:id/next (host) -> {attempt:Attempt|null}; leased offer, no arbitrary code.
POST /api/attempts/:id/heartbeat (host) -> {ok:true,leaseExpiresAt,cancelRequested:boolean}
POST /api/attempts/:id/complete (host) {result:{samples,inside,pi},usageSeconds:number} -> {accepted:boolean}
POST /api/jobs (owner, Idempotency-Key required) {template:"monte-carlo-pi-v1",samples:number,maxCostCents:number,execution:"private"|"marketplace"|"managed",targetHostId?:string} -> Job
POST /api/jobs/:id/cancel -> Job
GET /api/jobs/:id -> Job
GET /api/datasets -> {datasets:Dataset[]}
GET /api/ledger -> {entries:LedgerEntry[]}
GET /api/budget -> Budget
PUT /api/budget {coreMonthlyCents,founderMonthlyCents,engineeringMonthlyCents,reserveMonthlyCents,memberCount,usageContributionCents} -> Budget
GET /api/usage-budget -> {month,configured,monthlyLimitCents,reservedCents,settledCents,availableCents,mode,cashSpendingEnabled}
PUT /api/usage-budget {monthlyLimitCents} -> usage-budget object

The shared funding budget is a pricing forecast, not spending authorization.
Nonzero job reservations require the independently configured usage budget.
POST /mcp -> standard JSON-RPC initialize/tools/list/tools/call (pilot token auth, NOT full OAuth interoperability).
The stateless MCP JSON subset supports POST only, with a 16 KiB body ceiling
(declared and streamed bytes). The Worker authenticates before MCP parsing.
Malformed JSON uses JSON-RPC -32700; valid non-request JSON uses -32600.
Valid notifications return HTTP 202 and never execute tool calls. An optional
MCP-Protocol-Version header is validated and permitted in MCP-specific preflight.
No persistent sessions, SSE, automatic client onboarding or Claude compatibility
certification is implied. Quote delivery and paid execution remain disabled.

Host {id,name,platform,arch,cpuCores,memoryBytes,storageBytes,marketplaceEnabled,paused,approved,revoked,ownerActive,pqConfigured,pqVerified,lastSeenAt,createdAt}
Job {id,template,samples,maxCostCents,execution,targetHostId,status:"queued"|"leased"|"running"|"completed"|"cancelled"|"failed",createdAt,updatedAt,result?:object,attemptId?:string}
Attempt {id,jobId,hostId,template:"monte-carlo-pi-v1",samples,leaseExpiresAt,maxCostCents}
Dataset {id,name,provider,status:"disabled"|"licensed"|"demo",kind,description}
LedgerEntry {id,jobId?,kind,amountCents,createdAt,description}
Budget {coreMonthlyCents,founderMonthlyCents,engineeringMonthlyCents,reserveMonthlyCents,memberCount,usageContributionCents,sharedMonthlyCents}

## Security and honest scope
- Private-by-default host membership; public opts in separately and requires approved host plus fresh PQ verification.
- Host-reported PQ is not remote attestation. Owner review is separate; never claim malicious host secrecy.
- For end-to-end pilot the connector pulls jobs via outbound HTTPS; this is NOT protected by its incoming cloudflared tunnel.
  Production marketplace dispatch remains disabled pending authenticated signed
  grants, replay/revocation enforcement, independently verified tunnel behavior
  and reviewed job isolation.
- Default prod marketplace disabled by FEATURE_MARKETPLACE=false. Never fake readiness/earnings/live quotes.
- D1 conditional updates/batches + unique keys guard leases, one-use invites, idempotency and exactly-once ledger effects.
- Only fixed built-in Monte Carlo job is enabled initially; no arbitrary shell, URLs, Python or model downloads from jobs.
- Metering is illustrative non-cash ledger; external billing/payouts disabled, no automatic paid fallback.
- All unresolved requirements must be explicit in release status matrix.
