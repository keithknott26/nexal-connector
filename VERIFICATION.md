# Nexal verification snapshot

September 19, 2026. This snapshot supersedes historical KWK-era paths and names
in older workstream handoffs. It is not production certification.

## Completed locally after rename

- Platform strict TypeScript check and dashboard build passed.
- 310 TypeScript tests passed, including three new production-header tests.
- 18 setup-policy checks passed; shell syntax checks passed.
- 83 named Go connector test passes; race detector and vet passed.
- Go MLX command bridge race tests and vet passed.
- 34 Python policy/integrity tests passed with regenerated Nexal source hashes.
- Darwin ARM64 Go cross-build passed; not executed on a Mac here.
- Locked npm dependency audit reported zero known vulnerabilities at test time.
- Gitleaks 8.30.1 clean-export scan reported no unhandled findings. Its archive
  checksum was verified. Narrow test-only annotations document two false positives.
- Renamed Go connector plus renamed Wrangler/D1 platform passed a 25-check
  end-to-end run: completed fixed CPU job, duplicate handling, metering, pause,
  cancellation and gated services. Hardware telemetry was explicitly synthetic;
  no external spending occurred.

## Not completed

- Actual macOS Swift compilation, the new local-preview UI and Keychain behavior.
- Docker image runtime acceptance on the target Mac.
- GitHub-hosted CI: Actions intentionally disabled pending owner review.
- Signed/notarized distribution, actual PQ negotiation, distributed MLX, real
  feed delivery, cash payments or paid cloud execution.

See the sibling platform's `docs/PRODUCTION-GATES.md` before making a launch or
customer-readiness claim.
