# Nexal verification snapshot

September 19, 2026. This snapshot supersedes historical KWK-era paths and names
in older workstream handoffs. It is not production certification.

## Completed locally after rename

- Platform strict TypeScript check and dashboard build passed.
- 381 TypeScript tests passed in the sibling platform, including transactional
  audit, usage-budget and authenticated MCP error/size-boundary coverage.
- 24 setup-policy checks, 11 mocked installer scenarios and 9 subprocess launcher
  tests passed in the platform; shell syntax checks passed.
- 134 named Go connector test passes (72 top-level plus named subtests); race
  detector and vet passed. Agent/config suites passed five repeated race runs.
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

## Not completed

- Actual macOS Swift compilation, the new local-preview UI and Keychain behavior.
- Docker image runtime acceptance on the target Mac.
- GitHub-hosted CI: Actions intentionally disabled pending owner review.
- Signed/notarized distribution, actual PQ negotiation, distributed MLX, real
  feed delivery, cash payments or paid cloud execution.

See the sibling platform's `docs/PRODUCTION-GATES.md` before making a launch or
customer-readiness claim.
