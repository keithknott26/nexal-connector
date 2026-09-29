import SwiftUI

/// Reply of `nexal honeypot --action status|enable|disable`.
struct HoneypotReply: Decodable {
    struct Port: Decodable, Identifiable {
        let port: Int
        let service: String
        let listening: Bool
        let error: String?
        var id: Int { port }
    }
    struct Connection: Decodable, Identifiable {
        let observedAt: String
        let service: String
        let port: Int
        let sourceAddress: String
        let sourceClass: String
        var id: String { "\(observedAt)|\(port)|\(sourceAddress)" }
    }
    let enabled: Bool
    let status: String
    let ports: [Port]?
    let triggers: Int?
    let lastTriggeredAt: String?
    let pendingEvents: Int?
    let recent: [Connection]?
    let description: String?
}

enum HoneypotPresentation {
    static func status(_ reply: HoneypotReply) -> String {
        guard reply.enabled else { return "Off on this Mac" }
        switch reply.status {
        case "listening": return "Listening on this Mac"
        case "degraded": return "Some decoy services could not start"
        case "error": return "Honeypot needs attention"
        case "disabled": return "Off on this Mac"
        case "starting": return "Starting — opens within 15 seconds while neXal is running"
        default: return "Status not reported"
        }
    }

    static func sourceClass(_ value: String) -> String {
        switch value {
        case "mesh": return "neXal network"
        case "lan": return "Local network"
        default: return "Other"
        }
    }

    static func date(_ value: String?) -> String {
        guard let value else { return "Never" }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let date = formatter.date(from: value) ?? ISO8601DateFormatter().date(from: value)
        return date?.formatted(date: .abbreviated, time: .shortened) ?? value
    }
}

/// Explicit local opt-in; opening the panel only reads status and never opens a port.
struct HoneypotSettingsView: View {
    @EnvironmentObject private var model: AppModel
    @State private var state: HoneypotReply?
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("Honeypot", systemImage: "ant")
                .font(.subheadline.weight(.semibold))
            Text("Opens fake services on this Mac. Nothing legitimate should ever connect, so any connection from another computer is reported as an alert. It never runs commands or accepts logins.")
                .font(.caption).foregroundStyle(.secondary)
            if let state {
                HStack {
                    Text(HoneypotPresentation.status(state))
                        .font(.caption)
                        .foregroundStyle(state.enabled && state.status != "listening" ? Color.orange : Color.primary)
                    Spacer()
                    Button(state.enabled ? "Turn off" : "Turn on honeypot") {
                        Task { await perform(state.enabled ? "disable" : "enable") }
                    }.disabled(busy)
                }
                if state.enabled, let ports = state.ports, !ports.isEmpty {
                    VStack(alignment: .leading, spacing: 2) {
                        ForEach(ports) { port in
                            HStack(spacing: 6) {
                                Image(systemName: port.listening ? "circle.fill" : "exclamationmark.circle")
                                    .foregroundStyle(port.listening ? Color.green : Color.orange)
                                Text("\(port.service.uppercased()) · port \(String(port.port))")
                                Spacer()
                                Text(port.listening ? "Listening" : "Not listening\(port.error.map { " (\($0))" } ?? "")")
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }.font(.caption2)
                }
                if state.enabled || (state.triggers ?? 0) > 0 {
                    Text("Connections detected: \(state.triggers ?? 0) · Last: \(HoneypotPresentation.date(state.lastTriggeredAt))")
                        .font(.caption)
                    if let pending = state.pendingEvents, pending > 0 {
                        Text("\(pending) alert\(pending == 1 ? "" : "s") waiting to be sent").font(.caption2).foregroundStyle(.secondary)
                    }
                }
                if let recent = state.recent, !recent.isEmpty {
                    GroupBox("Recent connections") {
                        VStack(alignment: .leading, spacing: 4) {
                            ForEach(recent.prefix(10)) { connection in
                                HStack(spacing: 6) {
                                    Text(HoneypotPresentation.date(connection.observedAt))
                                    Text("\(connection.service.uppercased()):\(String(connection.port))")
                                    Text(connection.sourceAddress).textSelection(.enabled)
                                    Spacer()
                                    Text(HoneypotPresentation.sourceClass(connection.sourceClass)).foregroundStyle(.secondary)
                                }
                            }
                        }.font(.caption2)
                    }
                }
            }
            Button("Refresh honeypot status") { Task { await perform("status") } }
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
            state = try await model.honeypot(action: action)
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
    }
}
