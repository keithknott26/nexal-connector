# Nexal → Cloudflare staging: dry-run plan

**Status: nothing has been changed.** No Cloudflare resources created, no repo files
written, no DNS records touched. This document is the proposal only.

Checked on 2026-09-19:

- `nexal.systems` nameservers are already `igor.ns.cloudflare.com` / `laila.ns.cloudflare.com`.
  Confirmed against the `.systems` registry over RDAP, not a resolver lookup. DNS migration is complete.
- Zone contents, read from `igor.ns.cloudflare.com` directly: `NS`, plus Namecheap
  email-forwarding `MX` (eforward1–5.registrar-servers.com) and a matching SPF `TXT`.
  **No apex `A` record exists.** An earlier claim in this document that a parking record
  `A → 192.64.119.157` needed deleting was wrong — that answer came from an intercepting
  recursive resolver in the build sandbox, not from Cloudflare. No DNS deletion is required.
- The `MX`/`TXT` pair must be preserved; removing it breaks any Namecheap mail forwarding.
- Cloudflare API access authenticates correctly. The `6103 Invalid format for X-Auth-Key header`
  blocker recorded in `docs/CLOUDFLARE-DOMAIN-STATUS.md` no longer reproduces — that doc is stale.
- Account contains **0 Workers and 0 D1 databases**. Nothing provisioned yet.

---

## 0. Architecture decision and its cost

Chosen layout (split origin):

| Host | Serves | Cloudflare product |
|---|---|---|
| `app.nexal.systems` | dashboard SPA | Pages |
| `api.nexal.systems` | `/api/*`, `/mcp` | Worker `nexal-coordinator` |
| `nexal.systems` | unused for now | — |

This differs from `apps/coordinator/wrangler.jsonc` as committed, which serves the
dashboard from the Worker's `ASSETS` binding same-origin, and from
`docs/CLOUDFLARE-DOMAIN-STATUS.md`, which specifies `app.nexal.systems/api/*`.

The split is fully supported by the existing code — `allowedOrigin()` in
`apps/coordinator/src/index.ts` already implements an exact-match HTTPS origin
allowlist with proper preflight handling and no credentialed CORS. The costs are:

1. **Two deploy targets instead of one.** Pages project + Worker, each needing its own release step.
2. **Security headers must be duplicated.** The Worker sets CSP/HSTS/XFO on its own responses
   (`index.ts` lines 165–189). Those headers never reach the dashboard document once it is
   served by Pages. A `_headers` file must replicate them or the console ships without CSP.
3. **CORS becomes a live failure mode.** A wrong `ALLOWED_ORIGINS` value produces a 403
   `origin_denied` on every dashboard request. Same-origin cannot fail this way.
4. **`docs/CLOUDFLARE-DOMAIN-STATUS.md` becomes wrong** and needs updating, or the next
   person resuming from handoff docs will build the other topology.

No change needed to `apps/dashboard/src/api.ts` — it already reads `VITE_API_BASE` and uses
`credentials: 'same-origin'`, which is correct here since owner auth is a bearer token, not a cookie.

---

## 1. Resources to create

```
D1 database   nexal            (primary_location_hint: enam)
Worker        nexal-coordinator
Pages project nexal-dashboard
Custom domain api.nexal.systems → Worker
Custom domain app.nexal.systems → Pages
Secret        OWNER_TOKEN      (Worker secret, not a var)
```

Cloudflare free tier covers all of this at pilot volume. The `* * * * *` cron in
`wrangler.jsonc` fires 43,200 times/month against the 100k/day free request limit.

---

## 2. Diffs

### `apps/coordinator/wrangler.jsonc`

```diff
   "name": "nexal-coordinator",
   "main": "src/index.ts",
   "compatibility_date": "2026-09-01",
   "workers_dev": false,
-  "assets": {
-    "directory": "../dashboard/dist",
-    "binding": "ASSETS",
-    "not_found_handling": "single-page-application",
-    "run_worker_first": ["/api", "/api/*", "/mcp", "/mcp/*"]
-  },
+  "routes": [
+    { "pattern": "api.nexal.systems", "custom_domain": true }
+  ],
   "vars": {
     "ENVIRONMENT": "production",
     "FEATURE_MARKETPLACE": "false",
     "MARKETPLACE_GATES_VERIFIED": "false",
-    "DEVELOPMENT_SEED": "false"
+    "DEVELOPMENT_SEED": "false",
+    "ALLOWED_ORIGINS": "https://app.nexal.systems"
   },
   "d1_databases": [{
     "binding": "DB",
     "database_name": "nexal",
-    "database_id": "00000000-0000-0000-0000-000000000000",
+    "database_id": "<uuid returned by d1 create>",
     "migrations_dir": "migrations"
   }],
```

Removing `assets` means `env.ASSETS` is undefined. `index.ts` line 43 guards on
`if (env.ASSETS && ...)`, so non-API paths fall through to the 404 at line 121.
That is correct for an API-only origin — no code change required.

The `env.development` block is left untouched; local dev keeps its localhost origins.

### `apps/dashboard/public/_headers` — new file

Required to replace the security headers the Worker used to apply to the dashboard document.
Note `connect-src` must name the API origin explicitly; the committed Worker CSP uses
`connect-src 'self'`, which would block every cross-origin API call from the SPA.

```
/*
  X-Frame-Options: DENY
  X-Content-Type-Options: nosniff
  Referrer-Policy: no-referrer
  Permissions-Policy: camera=(), microphone=(), geolocation=()
  Strict-Transport-Security: max-age=31536000
  Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com https://api.fontshare.com; font-src 'self' https://fonts.gstatic.com https://*.fontshare.com; img-src 'self' data:; connect-src 'self' https://api.nexal.systems; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'
```

### `.gitignore`

```diff
 dev-secrets/
+owner-token.txt
```

### `docs/CLOUDFLARE-DOMAIN-STATUS.md`

The "Proposed hostname layout" section and the stale authentication-blocker section both
need rewriting to match the split topology and the working credential. Full replacement
text to follow once the topology is confirmed applied.

---

## 3. Command sequence

Run from the repo root on the M4. Steps 1–4 are safe/idempotent; step 5 is the first
one that serves traffic.

```sh
# 1. verify the tree is green before touching the account
npm ci
npm run typecheck
npm test
npm run build

# 2. create the database, then paste the returned uuid into wrangler.jsonc
npx wrangler d1 create nexal --location enam

# 3. migrations — staging first, both files
npx wrangler d1 migrations apply nexal --remote

# 4. owner secret. Generated locally, never pasted into chat or CLI args.
#    (I will write owner-token.txt into the repo root, gitignored, on your go-ahead.)
npx wrangler secret put OWNER_TOKEN < owner-token.txt
rm owner-token.txt

# 5. deploy the API worker + attach api.nexal.systems
npx wrangler deploy --config apps/coordinator/wrangler.jsonc

# 6. build and deploy the dashboard to Pages
VITE_API_BASE=https://api.nexal.systems npm run build --workspace=@nexal/dashboard
npx wrangler pages project create nexal-dashboard --production-branch main
npx wrangler pages deploy apps/dashboard/dist --project-name nexal-dashboard
# then attach app.nexal.systems as a Pages custom domain
```

No manual DNS record creation or deletion is part of this sequence. Attaching the two
custom domains is what creates the `api.` and `app.` records; the existing `MX`/`TXT`
records are untouched by it.

---

## 4. Post-deploy checks

```sh
curl -si https://api.nexal.systems/api/health            # 200, mode: production
curl -si https://api.nexal.systems/api/overview          # 401, not 503 (proves OWNER_TOKEN set)
curl -si -H 'Origin: https://evil.test' \
     https://api.nexal.systems/api/health                # 403 origin_denied
curl -si -H 'Origin: https://app.nexal.systems' \
     https://api.nexal.systems/api/health                # 200 + matching ACAO header
```

A 503 `owner_auth_unconfigured` on `/api/overview` means the secret did not take —
`requireOwner()` returns 503 before it ever checks the bearer token.

---

## 5. Explicitly out of scope

Per `docs/PRODUCTION-GATES.md` and `docs/RELEASE-STATUS.md`, this plan is the
"authorized cloud staging" step only. It does not touch, and must not be described
as completing:

- Swift/Keychain build acceptance on the actual M4 and M2
- Docker build/start/restart and volume restoration on the Mac
- Real cloudflared tunnel provisioning or PQ negotiation/downgrade-failure tests
- Signed/notarized Mac releases
- Public job isolation, marketplace, payments, live market-data feeds

`FEATURE_MARKETPLACE` and `MARKETPLACE_GATES_VERIFIED` stay `"false"`.
