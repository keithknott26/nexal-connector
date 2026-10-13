import AppKit
import SwiftUI

struct TimeMachineCredential: Decodable {
    let host: String
    let share: String
    let username: String
    let password: String
}

@MainActor
final class TimeMachineCredentialWindow: NSObject, NSWindowDelegate {
    static let shared = TimeMachineCredentialWindow()
    private var panel: NSPanel?
    private var timer: Timer?

    /// Open synchronously from the click, before fetching credentials. Keep the
    /// panel visible when the menu-bar popover closes or Settings gains focus.
    func begin() {
        close()
        let window = NSPanel(contentRect: NSRect(x: 0, y: 0, width: 570, height: 280),
                             styleMask: [.titled, .closable, .utilityWindow], backing: .buffered, defer: false)
        window.title = "Time Machine credentials"
        window.isReleasedWhenClosed = false
        window.hidesOnDeactivate = false
        window.level = .floating
        window.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        window.delegate = self
        window.contentView = NSHostingView(rootView:
            VStack(spacing: 12) {
                ProgressView()
                Text("Retrieving your backup credentials…")
            }.padding(24).frame(width: 530, height: 180))
        panel = window
        present()
    }

    func show(_ credential: TimeMachineCredential) {
        // Closing the panel while the request runs cancels presentation.
        guard let panel else { return }
        let hosting = NSHostingView(rootView: TimeMachineCredentialView(credential: credential))
        panel.contentView = hosting
        panel.setContentSize(hosting.fittingSize) // grow to fit the wrapped text
        present()
        timer = Timer.scheduledTimer(withTimeInterval: 120, repeats: false) { [weak self] _ in
            Task { @MainActor in self?.close() }
        }
    }

    func showError() {
        guard let panel else { return }
        panel.contentView = NSHostingView(rootView:
            VStack(alignment: .leading, spacing: 12) {
                Text("Could not retrieve credentials").font(.headline)
                Text("Check that backup is turned on and this Mac is online, then close this window and try again.")
            }.padding(24).frame(width: 530))
        present()
    }

    private func present() {
        panel?.center()
        NSApp.activate(ignoringOtherApps: true)
        panel?.makeKeyAndOrderFront(nil)
        panel?.orderFrontRegardless()
    }

    private func close() {
        timer?.invalidate()
        timer = nil
        panel?.contentView = nil
        panel?.close()
        panel = nil
    }

    func windowWillClose(_ notification: Notification) {
        timer?.invalidate()
        timer = nil
        panel?.contentView = nil
        panel = nil
    }
}

private struct TimeMachineCredentialView: View {
    let credential: TimeMachineCredential
    private var address: String { "smb://\(credential.host)/\(credential.share)" }
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            wrapped("1. Click Connect in Finder below (or in Finder choose Go \u{203A} Connect to Server\u{2026} and paste the address). Choose Registered User and sign in with the username and password below.")
            wrapped("2. In Time Machine settings, click Add Backup Disk and choose \(credential.share). If it is not listed, close the sheet and open it again.")
            row("Address", address)
            row("Username", credential.username)
            row("Password", credential.password)
            HStack {
                Button("Connect in Finder") {
                    var c = URLComponents()
                    c.scheme = "smb"; c.user = credential.username; c.host = credential.host; c.path = "/" + credential.share
                    if let url = c.url { NSWorkspace.shared.open(url) }
                }.buttonStyle(.borderedProminent)
                Button("Open Time Machine") {
                    if let url = URL(string: "x-apple.systempreferences:com.apple.Time-Machine-Settings.extension") { NSWorkspace.shared.open(url) }
                }
            }
            wrapped("This is your backup share password, not your Mac password. This window closes after two minutes.")
                .font(.caption).foregroundStyle(.secondary)
        }.padding(20).frame(width: 560)
    }
    private func wrapped(_ text: String) -> some View {
        Text(text).fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
    }
    private func row(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).frame(width: 75, alignment: .leading)
            Text(value).font(.system(.body, design: .monospaced)).textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
            Spacer()
            Button("Copy") {
                let board = NSPasteboard.general
                board.clearContents()
                board.setString(value, forType: .string)
                let revision = board.changeCount
                DispatchQueue.main.asyncAfter(deadline: .now() + 60) {
                    if board.changeCount == revision { board.clearContents() }
                }
            }.accessibilityLabel("Copy \(label.lowercased())")
        }
    }
}
