import SwiftUI
import AppKit

struct CanaryReply: Decodable {
    struct Signal: Decodable {
        let eventId: String
        let observedAt: String
        let detector: String
    }
    let latestSignal: Signal?
    let enabled: Bool
    let status: String
    let lastCheckedAt: String?
    /// Decoys installed on this Mac, and per-instance decoys registered here. Older connectors omit both.
    let hostWatermarks: Int?
    let subWatermarks: Int?
}

/// Explicit local opt-in; opening the panel never installs or arms a decoy.
struct CanarySettingsView: View {
    @EnvironmentObject private var model: AppModel
    @State private var state: CanaryReply?
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("Host watermarks", systemImage: "shield.lefthalf.filled")
                .font(.subheadline.weight(.semibold))
            Text("Host watermarks are harmless decoy files kept inside neXal’s own storage. If one changes or disappears, neXal sends an alert for you to review. Your documents are never touched.")
                .font(.caption).foregroundStyle(.secondary)
            Text("Alerts appear under Host watermarks on your iPhone and dashboard. neXal checks the decoy once a minute while it runs. It cannot tell if someone only reads the file, it does not scan for malware, and it never removes or blocks anything.")
                .font(.caption).foregroundStyle(.secondary)
            if let state {
                HStack {
                    Text(state.enabled ? (state.status == "alert" ? "Change detected · review the alert" : state.status == "watching" ? "On for this Mac" : "Needs attention") : "Off on this Mac")
                        .font(.caption)
                    Spacer()
                    Button(state.enabled ? "Turn off" : "Turn on watermarks") {
                        Task { await perform(state.enabled ? "disable" : "enable") }
                    }.disabled(busy)
                }
            }
            if let state, let hosts = state.hostWatermarks {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Host watermarks installed: \(hosts)")
                    Text("Sub-watermarks out there: \(state.subWatermarks ?? 0)")
                }
                .font(.caption).foregroundStyle(.secondary)
                .accessibilityElement(children: .combine)
            }
            if let signal = state?.latestSignal {
                GroupBox("Check this Mac") {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("A decoy file changed. This is a warning sign, not a malware diagnosis. Check this Mac with your own security software, make sure it is up to date, and follow its advice.")
                        Text("neXal does not scan for malware or confirm cleanup. Replacing the watermark does not resolve this alert.")
                            .foregroundStyle(.secondary)
                        Text("Detected: \(signal.observedAt)")
                            .textSelection(.enabled)
                        Button("Copy alert summary") {
                            NSPasteboard.general.clearContents()
                            NSPasteboard.general.setString("neXal Host watermark alert\nEvent: \(signal.eventId)\nDetected: \(signal.observedAt)\nSignal: \(signal.detector)\nCheck this Mac with your own security software. neXal does not scan for malware or identify which program made the change.", forType: .string)
                        }
                    }.font(.caption)
                }
            }
            Button("Refresh watermark status") { Task { await perform("status") } }
                .disabled(busy)
            if busy { ProgressView().controlSize(.small) }
            if let error { Text(error).font(.caption).foregroundStyle(.orange) }
        }
        .fixedSize(horizontal: false, vertical: true)
        .task { await perform("status") }
    }

    private func perform(_ action: String) async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        do {
            state = try await model.canary(action: action)
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }
}
