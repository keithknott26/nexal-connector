# Accept jobs now: native menu-bar acceptance

This checklist covers the updated SwiftUI button, not production dispatch or MLX.
The source is integrated into the normal NexalMac target and packaging recipe.
No separate app, executor terminal, recovery-mode flag or elevated privilege is
required. It remains an unsigned engineering preview.

## Build prerequisites

Update both acceptance worktrees with the platform's
`docs/UPDATE-PRIVATE-PREVIEW-V2.md` procedure. Keep their `COMPATIBILITY` markers
identical and apply local migration `0005_manual_private_acceptance.sql` through
the normal setup command. Preserve existing configuration and enrollment.
Quit the older app/identified executor before replacing the app.

The normal `setup-macos.sh --native --no-start` rebuild runs `swift test` and
packages the menu-bar app with the matching Go helper. Use the newly built app,
not a retained copy from another folder. If its pinned helper changed, explicitly
select **Use bundled nexal** again after review.

## Native interaction checks

- In the existing local-preview profile, an enrolled host shows **Accept jobs
  now** prominently under Owner controls. With no running daemon, one click
  starts a daemon and requests permission; with one running, it reuses it.
- The accessibility identifier is `accept-jobs-now`. Verify keyboard focus and
  VoiceOver announcement, and that busy state prevents concurrent clicks.
- After the connector confirms an unpaused, supported, unexpired permission,
  the label becomes **Accepting private jobs**, with an expiry in local time.
  Repeated clicks are disabled and do not extend the grant.
- Real owner-active telemetry remains visible. A memory blocker remains visible;
  neither owner reserve nor resource allowance is changed by the button.
- Submit a zero-cost private CPU job that fits the actual resource limits.
  Confirm its lease/result in the coordinator. Do not mark execution successful
  merely because the button changes state.
- **Pause and cancel work** clears permission and cancels active work. The
  button becomes available again after refreshed status confirms the pause.
- After permission expiry, periodic status refresh returns the button to its
  inactive state. No automatic renewal is permitted.
- Production profile, absent selection/configuration, unenrolled status and an
  old connector show an explanatory disabled state. They never grant consent.
- Disconnect/stop the connector during a request. The app must not optimistically
  claim acceptance; it reports an unconfirmed outcome. Refresh safely because
  the request may have reached the connector before confirmation failed.
- A slow first daemon startup must not cause a second process launch. If the
  initial status read fails, retry Start / Connect against the app-owned process.

## Verification status

Seven new XCTest methods cover presentation, missing/expired/malformed deadlines,
pause and consent requirements, setup state, and old/unenrolled connectors.
They accompany existing CLI argument and status-decoding tests. They have
**not run in the Linux authoring environment**, and SwiftUI interactions require
native Mac acceptance. Go tests validate the underlying acceptance endpoint;
they do not substitute for compiling or clicking the native UI.

No hosted macOS CI job was requested: the repository keeps it manual because
private hosted runners may incur charges. No user's Mac was modified remotely.
