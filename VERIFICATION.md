# Nexal verification snapshot

September 19, 2026. This snapshot supersedes historical KWK-era paths and names
in older workstream handoffs. It is not production certification.

## Completed locally after rename

- Platform strict TypeScript check and dashboard build passed.
- 354 TypeScript tests passed in the sibling platform, including usage-budget
  controls and authenticated MCP error/size-boundary coverage.
- 18 setup-policy checks passed; shell syntax checks passed.
- 106 named Go connector test passes (63 top-level plus named subtests); race
  detector and vet passed. Agent/config suites passed five repeated race runs.
- Go MLX command bridge race tests and vet passed.
- 34 Python policy/integrity tests passed with regenerated Nexal source hashes.
- Darwin ARM64 Go cross-build passed; not executed on a Mac here.
- Locked npm dependency audit reported zero known vulnerabilities at test time.
- Gitleaks 8.30.1 clean-export scan reported no unhandled findings. Its archive
  checksum was verified. Narrow test-only annotations document two false positives.
- Updated Go connector plus Wrangler/D1 platform passed an integration run with 38 checks
  end-to-end run: completed fixed CPU job, duplicate handling, metering, pause,
  cancellation, policy read/write/restore, MCP errors and gated services.
  Polling can change the check count. Hardware telemetry was explicitly synthetic;
  no external spending occurred.
- The platform's `scripts/verify-local.sh` checks both repositories together.
  It does not install software, deploy, enable billing or prove native Mac behavior.

## Not completed

- Actual macOS Swift compilation, the new local-preview UI and Keychain behavior.
- Docker image runtime acceptance on the target Mac.
- GitHub-hosted CI: Actions intentionally disabled pending owner review.
- Signed/notarized distribution, actual PQ negotiation, distributed MLX, real
  feed delivery, cash payments or paid cloud execution.

See the sibling platform's `docs/PRODUCTION-GATES.md` before making a launch or
customer-readiness claim.
