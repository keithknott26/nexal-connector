import SwiftUI

struct CanaryReply: Decodable {
    let enabled: Bool
    let status: String
    let lastCheckedAt: String?
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
            Text("A canary is a harmless decoy file kept inside neXal’s own storage. If it changes or disappears, neXal sends an alert for you to review. Your documents stay untouched.")
                .font(.caption).foregroundStyle(.secondary)
            Text("Reports appear in Host watermarks on your iPhone and dashboard. This checks for changes once a minute while the connector runs. It does not detect someone reading the file or replace a malware scanner. Nothing is automatically removed or blocked.")
                .font(.caption).foregroundStyle(.secondary)
            if let state {
                HStack {
                    Text(state.enabled ? (state.status == "alert" ? "Change detected · review security alerts" : state.status == "watching" ? "Enabled on this Mac" : "Monitoring needs attention") : "Off on this Mac")
                        .font(.caption)
                    Spacer()
                    Button(state.enabled ? "Turn off" : "Enable watermarks") {
                        Task { await perform(state.enabled ? "disable" : "enable") }
                    }.disabled(busy)
                }
            }
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
