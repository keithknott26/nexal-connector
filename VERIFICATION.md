# Nexal verification snapshot

September 19, 2026. This snapshot supersedes historical KWK-era paths and names
in older workstream handoffs. It is not production certification.

## Completed locally after rename

- Platform strict TypeScript check and dashboard build passed.
- 424 TypeScript tests passed in the sibling platform across 11 files, including
  transactional audit, admission integrity and MCP/provider resilience coverage.
- 24 setup-policy checks, 20 mocked installer scenarios and 18 subprocess launcher
  tests passed in the platform; shell syntax checks passed.
- 190 named Go connector test passes (89 top-level plus named subtests); race
  detector and vet passed. Agent/client/config/tunnel/diagnostics suites passed
  five repeated race runs.
- Go MLX command bridge race tests and vet passed.
- 34 Python policy/integrity tests passed with regenerated Nexal source hashes.
- Darwin ARM64 Go cross-build passed; not executed on a Mac here.
- Locked npm dependency audit reported zero known vulnerabilities at test time.
- Gitleaks 8.30.1 clean-export scan reported no unhandled findings. Its archive
  checksum was verified. Narrow test-only annotations document two false positives.
- Updated Go connector plus Wrangler/D1 platform passed an integration run with 41 checks
  end-to-end run: completed fixed CPU job, duplicate handling, metering, pause,
  cancellation, policy read/write/restore, MCP errors, audit transitions,
  sanitized doctor output and gated services.
  Polling can change the check count. Hardware telemetry was explicitly synthetic;
  no external spending occurred.
- The platform's `scripts/verify-local.sh` checks both repositories together.
  It does not install software, deploy, enable billing or prove native Mac behavior.
- Failed Swift packaging preserved an existing app in a fault-injected test.
  Successful native compilation/plist validation and bundle publication have
  not been tested here. Do not treat simulated tool outputs as Mac acceptance.
- Saved configuration now requires explicit, non-null `paused`; generated
  configurations already comply. Pending-pull cancellation, observation expiry,
  private-file handling and tunnel evidence have new regressions.
- Coordinator migration 0004 is required for this source revision. The platform
  applied it locally and tested upgrades, not remotely in Cloudflare.

## Not completed

- Actual local-preview UI and Keychain behavior. Subsequent owner-supplied M4
  output confirms native Swift compilation, seven XCTest cases and initial app
  packaging passed; see MAC-ACCEPTANCE.md for provenance and remaining checks.
- Docker image runtime acceptance on the target Mac.
- GitHub-hosted CI: Actions intentionally disabled pending owner review.
- Signed/notarized distribution, actual PQ negotiation, distributed MLX, real
  feed delivery, cash payments or paid cloud execution.

See the sibling platform's `docs/PRODUCTION-GATES.md` before making a launch or
customer-readiness claim.
