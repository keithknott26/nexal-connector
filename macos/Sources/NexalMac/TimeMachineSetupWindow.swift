import AppKit

/// Keep setup feedback visible even after the menu-bar popover closes.
@MainActor
final class TimeMachineSetupWindow {
    static let shared = TimeMachineSetupWindow()
    private var alert: NSAlert?

    func begin() {
        alert?.window.close()
        let alert = NSAlert()
        alert.messageText = "Setting up Time Machine…"
        alert.informativeText = "Administrator approval lets macOS add a backup destination; it is separate from Full Disk Access. Adding the backup disk can take a few minutes. The result will appear here."
        alert.addButton(withTitle: "Hide")
        alert.buttons.first?.target = self
        alert.buttons.first?.action = #selector(close)
        show(alert)
    }

    /// A non-modal NSAlert is never laid out on its own (truncated text, a placeholder
    /// checkbox, an empty button): lay it out before ordering it front.
    private func show(_ alert: NSAlert) {
        self.alert = alert
        alert.layout()
        alert.window.level = .floating
        alert.window.center()
        alert.window.makeKeyAndOrderFront(nil)
    }

    func finish(error: String?) {
        alert?.window.close()
        let alert = NSAlert()
        alert.messageText = error == nil ? "Backup disk added" : "Time Machine setup needs attention"
        alert.informativeText = error ?? "Your backup disk was added. Open Time Machine settings to review it."
        alert.alertStyle = error == nil ? .informational : .warning
        alert.addButton(withTitle: "Done")
        alert.buttons[0].target = self
        alert.buttons[0].action = #selector(close)
        alert.addButton(withTitle: "Open Time Machine")
        alert.buttons[1].target = self
        alert.buttons[1].action = #selector(openTimeMachine)
        if error?.hasPrefix("Time Machine reported that Full Disk Access is required") == true {
            alert.addButton(withTitle: "Review Full Disk Access…")
            alert.buttons[2].target = self
            alert.buttons[2].action = #selector(openPermissions)
        }
        show(alert)
        NSApp.activate(ignoringOtherApps: true)
        // Only a successful setup opens Time Machine settings by itself.
        if error == nil { openTimeMachine() }
    }

    @objc private func close() { alert?.window.close() }
    @objc private func openPermissions() { PermissionHelpWindow.shared.showFullDiskAccess() }
    @objc private func openTimeMachine() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.Time-Machine-Settings.extension") {
            NSWorkspace.shared.open(url)
        }
    }
}
