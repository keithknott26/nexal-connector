import AppKit
import SwiftUI

/// Settings › General › Remote access: shows whether Remote Login (SSH), Screen Sharing
/// (VNC) and File Sharing (SMB) are on for this Mac, explains why they matter, and turns
/// SSH and Screen Sharing on with one administrator prompt. Anything macOS will not let
/// an app switch on (or that still needs per-user access choices) opens the exact
/// System Settings pane instead.
struct SharingServicesSettingsView: View {
    @State private var probe: LocalServiceProbe.Result?
    @State private var working: Service?
    @State private var message: String?

    enum Service: String, CaseIterable, Identifiable {
        case remoteLogin, screenSharing, fileSharing
        var id: String { rawValue }
        var title: String {
            switch self {
            case .remoteLogin: "Remote Login (SSH)"
            case .screenSharing: "Screen Sharing (VNC)"
            case .fileSharing: "File Sharing (SMB)"
            }
        }
        var purpose: String {
            switch self {
            case .remoteLogin: "Lets your other computers and iPhone open a terminal on this Mac."
            case .screenSharing: "Lets your other computers and iPhone see and control this Mac's screen."
            case .fileSharing: "Lets your other computers and iPhone open this Mac's shared folders."
            }
        }
        /// The System Settings › General › Sharing anchor for this service.
        var settingsURL: URL? {
            let anchor = switch self {
            case .remoteLogin: "Services_RemoteLogin"
            case .screenSharing: "Services_ScreenSharing"
            case .fileSharing: "Services_PersonalFileSharing"
            }
            return URL(string: "x-apple.systempreferences:com.apple.Sharing-Settings.extension?\(anchor)")
        }
        /// Shell commands run as root to turn the service on; nil when only System
        /// Settings can do it (File Sharing needs share and user choices).
        var enableCommand: String? {
            switch self {
            case .remoteLogin:
                // systemsetup needs Full Disk Access on recent macOS; launchctl is the fallback.
                "/usr/sbin/systemsetup -f -setremotelogin on >/dev/null 2>&1 || "
                    + "(/bin/launchctl enable system/com.openssh.sshd; "
                    + "/bin/launchctl bootstrap system /System/Library/LaunchDaemons/ssh.plist 2>/dev/null; true)"
            case .screenSharing:
                "/bin/launchctl enable system/com.apple.screensharing; "
                    + "/bin/launchctl bootstrap system /System/Library/LaunchDaemons/com.apple.screensharing.plist 2>/dev/null; true"
            case .fileSharing:
                nil
            }
        }
        func isOn(_ r: LocalServiceProbe.Result) -> Bool {
            switch self {
            case .remoteLogin: r.remoteLogin
            case .screenSharing: r.screenSharing
            case .fileSharing: r.fileSharing
            }
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Your other computers and iPhone can only reach the services that are turned on here. neXal never opens them to the internet: they are reachable only over your private network.")
                .font(.caption).foregroundStyle(.secondary)
            ForEach(Service.allCases) { service in
                row(service)
            }
            if let message {
                Text(message).font(.callout).foregroundStyle(.secondary).textSelection(.enabled)
            }
            Text("After turning one on, check “Allow access for” in System Settings › General › Sharing › (i) so your user account is allowed. For Screen Sharing from iPhone, turn on “VNC viewers may control screen with password” there too.")
                .font(.caption).foregroundStyle(.secondary)
            Button("Check again") { Task { await refresh() } }
                .disabled(working != nil)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .task { await refresh() }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            Task { await refresh() }
        }
    }

    @ViewBuilder
    private func row(_ service: Service) -> some View {
        let on = probe.map { service.isOn($0) }
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Image(systemName: on == true ? "checkmark.circle.fill" : "circle.dashed")
                .foregroundStyle(on == true ? .green : .orange)
                .accessibilityLabel(on == true ? "On" : on == false ? "Off" : "Checking")
            // One line: "Remote Login (SSH)   On"; the purpose shows only while it is off.
            Text(service.title).lineLimit(1).fixedSize()
            Text(on == nil ? "Checking…" : on == true ? "On" : "Off")
                .foregroundStyle(on == true ? .green : .secondary)
            if on == false {
                Text(service.purpose).font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.tail)
            }
            Spacer()
            if on == false {
                if service.enableCommand != nil {
                    Button(working == service ? "Turning on…" : "Turn On") { Task { await enable(service) } }
                        .buttonStyle(.borderedProminent)
                        .disabled(working != nil)
                }
                Button("Open Settings") { open(service) }
                    .disabled(working != nil)
            }
        }
    }

    private func refresh() async {
        probe = await LocalServiceProbe.run()
    }

    private func open(_ service: Service) {
        AppDiagnostics.ui("sharing settings opened", ["service": service.rawValue])
        if let url = service.settingsURL { NSWorkspace.shared.open(url) }
    }

    private func enable(_ service: Service) async {
        guard let command = service.enableCommand, working == nil else { return }
        working = service; message = nil
        defer { working = nil }
        AppDiagnostics.ui("turning on sharing service", ["service": service.rawValue])
        let prompt = "neXal needs to turn on \(service.title) so your other computers and iPhone can reach this Mac over your private network."
        let outcome = await Task.detached(priority: .userInitiated) { () -> String? in
            Self.runPrivileged(command, prompt: prompt)
        }.value
        // Services take a moment to start listening.
        for _ in 0..<10 {
            try? await Task.sleep(for: .milliseconds(500))
            await refresh()
            if let probe, service.isOn(probe) { break }
        }
        if let probe, service.isOn(probe) {
            message = "\(service.title) is on."
            AppDiagnostics.ui("sharing service on", ["service": service.rawValue])
        } else if outcome == "cancelled" {
            message = "Not changed. You can also turn it on in System Settings."
        } else {
            message = "macOS didn't turn on \(service.title) from here\(outcome.map { " (\($0))" } ?? ""). Opening System Settings: switch it on there."
            AppDiagnostics.error("sharing service enable failed", ["service": service.rawValue, "error": outcome ?? "not listening"])
            open(service)
        }
    }

    /// Runs `command` as root through the standard macOS administrator prompt.
    /// Returns nil on success, "cancelled", or a short error.
    nonisolated private static func runPrivileged(_ command: String, prompt: String) -> String? {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        process.arguments = [
            "-e", "on run argv",
            "-e", "do shell script (item 1 of argv) with prompt (item 2 of argv) with administrator privileges",
            "-e", "end run",
            command, prompt,
        ]
        let errors = Pipe()
        process.standardError = errors
        process.standardOutput = FileHandle.nullDevice
        do { try process.run() } catch { return error.localizedDescription }
        process.waitUntilExit()
        guard process.terminationStatus != 0 else { return nil }
        let text = String(decoding: errors.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        if text.contains("-128") || text.localizedCaseInsensitiveContains("cancel") { return "cancelled" }
        return String(text.suffix(200))
    }
}
