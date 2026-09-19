# Enrollment confirmation UI acceptance

This change replaces the empty placeholder after successful enrollment with a
fixed masked display, an Enrolled label, and an explicit Use another code action.
The actual invitation is still cleared on success or failure and is never used
as the masked display value. No credentials are added to preferences or logs.

## Verification status

- Eight XCTest regression cases added for confirmation state, configuration
  isolation, status restoration, missing host IDs, and explicit replacement.
- Native compilation, XCTest execution, VoiceOver behavior and visual rendering
  require macOS acceptance. They have not been verified in the Linux workspace.
- No changes to execution mode, contribution policy, memory admission or tunnels.

## Mac acceptance checklist

- Build and test using `bash macos/scripts/package-app.sh` from the repository root.
- New configuration: the editable one-use code field appears with no success mask.
- Successful enrollment: input is cleared, a 24-character masked field and Enrolled
  label appear, and the enrollment submission button is no longer offered.
- Invalid/expired code: no success confirmation is introduced; input is cleared.
- Use another code: editable empty input returns and consent must be checked again.
- Periodic status refresh does not replace an open replacement-code form.
- Switching preview/production does not carry an entered code, consent or the
  other configuration's confirmation into the newly selected configuration.
- Restart and attach/refresh: a status response with a host ID restores the
  confirmation for that configuration, without needing the consumed invitation.
- VoiceOver reads the Enrolled label and explanation, not the fixed mask value.
- Existing private resource, pause and execution behavior is unchanged.
