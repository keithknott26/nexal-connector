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
        window.collectionBehavior = [.moveToActiveSpace, .fullScreenAuxiliary]
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
        panel.contentView = NSHostingView(rootView: TimeMachineCredentialView(credential: credential))
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
                Text("Check that backup is enabled and the coordinator is reachable, then close this window and try again.")
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
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Choose Registered User in the macOS share login.")
            Text("Server: \(credential.host)\nShare: \(credential.share)")
                .font(.caption).textSelection(.enabled)
            row("Username", credential.username)
            row("Password", credential.password)
            Text("This is your backup share password, not your Mac password. This window closes after two minutes.")
                .font(.caption).foregroundStyle(.secondary)
        }.padding(20).frame(width: 530)
    }
    private func row(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).frame(width: 75, alignment: .leading)
            Text(value).font(.system(.body, design: .monospaced)).textSelection(.enabled)
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
