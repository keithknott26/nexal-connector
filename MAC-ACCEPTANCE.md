# neXal native Mac acceptance

Evidence date: September 19, 2026. The owner ran the acceptance commands on the
target Mac and supplied their terminal output. These are user-reported native
results, not remote execution or independent UI observation by the assistant.

## Tested source and environment

- Platform: `d601629`, isolated detached acceptance worktree.
- Connector: `76345d9`, sibling isolated detached acceptance worktree.
- Hardware: owner previously identified an M4 Mac mini with 24 GB RAM.
- macOS 27.0, build 26A428; arm64 target.
- Xcode 27.0, build 27A266a; Swift 6.4.
- Node 24.21.0 selected from its versioned Homebrew installation.
- Go selected from the versioned `go@1.26` installation; exact patch version
  was not printed in the supplied build output.
- Original staging checkout, configuration changes and untracked files were
  kept separate from the acceptance worktrees.

## Passed in supplied native output

- Xcode first-launch readiness returned zero after the owner completed setup.
- Targeted Homebrew Node installation completed after approval. Homebrew also
  installed/upgraded shared dependencies; this was not a dependency-free change.
- Installer selected versioned tools without requiring global Node/Go relinking.
- Locked npm install completed; audit reported zero known vulnerabilities.
- TypeScript checks and all 424 tests across 11 files passed.
- Dashboard production build passed.
- Local D1 migrations 0001 through 0004 applied successfully to acceptance state.
- Go vet and race-enabled package tests completed successfully; all seven
  connector packages reported `ok`.
- Swift release compilation and debug test build completed successfully.
- Seven XCTest cases passed with zero failures. The separate Swift Testing
  runner reported zero tests; this does not negate the seven XCTest results.
- Staged app Info.plist validation passed and app packaging completed.
- Installer exited with `--no-start`; it did not start the new coordinator.

## Warnings and boundaries

- Homebrew listed unrelated untrusted taps. No additional tap trust is needed
  merely to continue this acceptance flow; do not grant broad trust as a workaround.
- npm reported install scripts lacking allowScripts coverage. The supplied run
  still completed its tests/build. This is not evidence of a reviewed, closed
  dependency supply chain; policy review remains separate acceptance work.
- One sanitized coordinator error occurred inside the deliberate audit rollback
  test, which passed. It is not a reported live coordinator failure.
- The app is not Developer ID signed or notarized for distribution.
- Docker already occupied loopback port 8787. No existing container was stopped.
- Current Swift local-preview initialization is fixed to `http://127.0.0.1:8787`.
  Do not silently attach it to an unknown Docker service or claim alternate-port
  UI onboarding is supported. Identify the existing listener before switching.

## Still pending

- Actual app launch, menu-bar UI and executable selection.
- Native coordinator startup, health, dashboard access and clean shutdown.
- Paused initialization, enrollment, credential handling and restart.
- Keychain acceptance: local preview deliberately uses restricted file credentials,
  so successful preview enrollment alone cannot validate Keychain.
- Real telemetry, owner reclaim, pause/resume, sleep and network-loss recovery.
- Failed/tampered executable behavior, duplicate start and accessibility checks.
- Successful upgrade over an existing installed app and interruption recovery.
- Docker-mode acceptance, M2 acceptance, signing/notarization and distribution.
- Live tunnel/PQ verification, public workload isolation, distributed MLX,
  licensed live feeds, payments and paid cloud fallback remain gated.

Do not mark the full native checklist complete based only on the build log.
