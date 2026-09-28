# Permission guidance

Full Disk Access uses `PermissionHelpWindow.shared.showFullDiskAccess()` from
Time Machine, its permission error recovery, and General Settings. The retained
compact floating panel opens Full Disk Access automatically and stays visible when System Settings activates. It follows the Settings window using window-server bounds metadata, positioning below it when space permits and clamping to the visible screen otherwise. No screen recording or Accessibility permission is requested. Tracking stops when the guide closes. Drag the running
application bundle into the Full Disk Access list and enable its switch; Finder
and the Settings + button provide an alternative. The guide never claims a grant
or changes the macOS permission database. Restart the app if macOS requests it.

Use this floating app-card pattern for future macOS privacy permissions whose
Settings panes accept application bundles. Always supply the actual running app
URL, a reason, the appropriate Settings link, and a non-drag alternative. Do not
present a popover-bound sheet for an action that requires switching applications.

Other permission workflows retain the system mechanism that actually grants
access:

- Login Items: native Login Items Settings and approval toggle; status refreshed
  when the app becomes active.
- Networking installation, firewall/wake changes, and guest expiry guards:
  macOS administrator authentication with an explanation of the requested action.
- Scanner roots and file selections: native file/folder picker.
- iOS camera and notifications: native authorization prompts and supported recovery.
- Browser camera/microphone: browser permission prompts.

Dragging an icon cannot approve these other mechanisms. Never substitute a
permission-looking UI for a real system grant, or request unrelated permissions
in advance. Full Disk Access also does not resolve SMB account/password errors.

Manual acceptance: open the guide from General Settings and Time Machine;
activate Settings and verify the guide remains visible; drag the icon to Full
Disk Access, enable it, follow any relaunch prompt, and retry Time Machine. Verify
Show App in Finder, closing/reopening the guide, and single-window reuse. A
successful build does not verify a user-approved Full Disk Access grant.
