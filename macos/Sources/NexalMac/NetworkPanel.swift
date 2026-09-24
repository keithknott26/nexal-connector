import AppKit
import SwiftUI

/// The customer-facing connector has one job: connect this Mac to neXal.
/// Implementation defaults and account policy do not belong in this menu.
struct NetworkPanel: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                header
                switch model.networkScreen {
                case .needsConnector(let reason): blocked(reason)
                case .notLinked: readyToPair
                case .linking(let linking): pairing(linking)
                case .linked(let network): connected(network)
                }
                message
                footer
            }
            .padding(20)
        }
        .frame(width: 460, height: 700)
        .task {
            while !Task.isCancelled {
                await model.refresh()
                do { try await Task.sleep(for: .seconds(5)) } catch { break }
            }
        }
    }

    private var header: some View {
        HStack(spacing: 10) {
            Image(systemName: model.menuBarSymbol).font(.title2).foregroundStyle(.tint).frame(width: 30)
            VStack(alignment: .leading, spacing: 1) {
                Text("neXal Connector").font(.headline)
                Text(model.hostNameDisplay).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if model.busy { ProgressView().controlSize(.small) }
        }
    }

    private var readyToPair: some View {
        VStack(alignment: .leading, spacing: 14) {
            step(number: "1", title: "Sign in on your iPhone",
                 detail: "Open neXal on your iPhone and sign in with Apple.", symbol: "apple.logo")
            step(number: "2", title: "Pair this Mac",
                 detail: "Show a one-time code here, then scan it with the neXal iPhone app.", symbol: "qrcode")
            Button { Task { await model.startPairing() } } label: {
                Label("Show pairing code", systemImage: "qrcode").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .disabled(model.busy || model.pairingUnavailableReason != nil)
            .accessibilityIdentifier("show-pairing-code")
            if let reason = model.pairingUnavailableReason {
                Label(reason, systemImage: "exclamationmark.triangle")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private func pairing(_ linking: LinkingState) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Scan to connect this Mac").font(.title3.weight(.semibold))
            Text("In the neXal iPhone app, choose Add Computer and scan this code.")
                .font(.caption).foregroundStyle(.secondary)
            if let symbol = model.pairing?.symbol {
                HStack { Spacer(minLength: 0); PairingCodeView(symbol: symbol, isLive: !linking.isExpired); Spacer(minLength: 0) }
            }
            if let code = model.pairing?.manualCode {
                VStack(spacing: 5) {
                    Text("PAIR MANUALLY").font(.caption2.weight(.semibold)).foregroundStyle(.secondary)
                    Text(code).font(.system(.title, design: .monospaced).weight(.semibold))
                        .textSelection(.enabled).accessibilityLabel("Manual pairing code \(code)")
                    Text("If scanning does not work, tap Pair manually on your iPhone and enter this code.")
                        .font(.caption).foregroundStyle(.secondary).multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity).padding(12)
                .background(.secondary.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
            }
            Text(linking.statusLine).font(.caption).foregroundStyle(.secondary)
            HStack {
                Button("Cancel", role: .cancel) { Task { await model.cancelPairing() } }
                Spacer()
                Button("New code") { Task { await model.startPairing() } }
            }
        }
    }

    private func connected(_ network: NetworkState) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            Label("This Mac is connected", systemImage: "checkmark.circle.fill")
                .font(.title3.weight(.semibold)).foregroundStyle(.green)
            if let mesh = model.status?.mesh {
                meshSummary(mesh)
            } else {
                status(network.tunnel)
                Text("The networking service has not reported peer routes yet.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            Divider()
            Text("Connected computers").font(.subheadline.weight(.semibold))
            if let peers = model.status?.mesh?.peers, !peers.isEmpty {
                ForEach(peers) { peer in host(peer) }
            } else if network.peers.isEmpty {
                Text("No other computers are connected yet.").font(.caption).foregroundStyle(.secondary)
            } else {
                ForEach(network.peers) { peer in
                    HStack(alignment: .top, spacing: 8) {
                        Circle().fill(peer.reachability.isConnectable ? Color.green : Color.secondary.opacity(0.4))
                            .frame(width: 7, height: 7).padding(.top, 5)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(peer.name.isEmpty ? peer.id : peer.name)
                            Text(peer.displayAddress ?? peer.statusNote)
                                .font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                    }
                }
            }
            Divider()
            activityGraphs
            if model.hasUnfinishedEnrollment {
                Divider()
                Button("Clear failed pairing and start over", role: .destructive) {
                    Task { await model.resetUnfinishedPairing() }
                }.disabled(model.busy)
            }
        }
    }

    private func meshSummary(_ mesh: ConnectorStatus.MeshStatus) -> some View {
        VStack(alignment: .leading, spacing: 7) {
            LabeledContent("Tunnel", value: mesh.lifecycle.capitalized)
            LabeledContent("Post-quantum protection", value: pqLabel(mesh.pq))
            LabeledContent("Network path", value: pathSummary(mesh.peers))
            if let step = mesh.authenticationStep, !step.isEmpty {
                LabeledContent("Connection step", value: step)
            }
            if let updated = mesh.updatedAt, !updated.isEmpty {
                Text("Evidence updated \(updated)").font(.caption2).foregroundStyle(.secondary)
            }
        }
        .font(.caption)
        .padding(12)
        .background(.secondary.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
        .accessibilityIdentifier("mesh-summary")
    }

    private func host(_ peer: ConnectorStatus.MeshPeer) -> some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: 6) {
                LabeledContent("Connection", value: peer.lifecycle.capitalized)
                LabeledContent("Post-quantum protection", value: pqLabel(peer.pq))
                LabeledContent("Current path", value: routeLabel(peer))
                if let step = peer.authenticationStep, !step.isEmpty { LabeledContent("Authentication", value: step) }
                if let latency = peer.latencyMs { LabeledContent("Latency", value: latency.formatted(.number.precision(.fractionLength(0))) + " ms") }
                if let loss = peer.packetLossPercent { LabeledContent("Packet loss", value: loss.formatted(.number.precision(.fractionLength(1))) + "%") }
                LabeledContent("Traffic", value: "↑ \(bytes(peer.traffic.sentBytes)) · ↓ \(bytes(peer.traffic.receivedBytes))")
                if let handshake = peer.lastHandshakeAt { LabeledContent("Last handshake", value: handshake) }
                if let verified = peer.pqVerifiedAt { LabeledContent("PQ last verified", value: verified) }
                if let hostname = peer.hostname?.hostname { LabeledContent("neXal address", value: hostname) }
                Text("Operating system was not reported. This entry may be a Mac or Linux host.")
                    .font(.caption2).foregroundStyle(.secondary)
            }
            .font(.caption)
            .padding(.top, 6)
        } label: {
            HStack(spacing: 8) {
                Circle().fill(peer.lifecycle == "connected" ? Color.green : Color.orange)
                    .frame(width: 7, height: 7)
                VStack(alignment: .leading, spacing: 1) {
                    Text(peer.name.isEmpty ? peer.id : peer.name)
                    Text("\(pqLabel(peer.pq)) · \(routeLabel(peer))")
                        .font(.caption2).foregroundStyle(.secondary)
                }
            }
        }
        .accessibilityIdentifier("host-\(peer.id)")
    }

    private var activityGraphs: some View {
        DisclosureGroup("Activity graphs") {
            VStack(alignment: .leading, spacing: 14) {
                ChartCard(title: "Memory policy", caption: ConnectorHistory.memoryCaption,
                          isEmpty: model.history.memoryPoints.isEmpty,
                          emptyMessage: "No memory-policy samples yet.") {
                    SeriesChart(points: model.history.memoryPoints, unit: "MiB")
                }
                ChartCard(title: "Job activity", caption: ConnectorHistory.jobActivityLimits,
                          isEmpty: model.history.jobActivityPoints.isEmpty,
                          emptyMessage: "No connector activity samples yet.") {
                    SeriesChart(points: model.history.jobActivityPoints, unit: "Count")
                }
                ChartCard(title: "Owner activity", caption: ConnectorHistory.ownerActivityCaption,
                          isEmpty: model.history.ownerActivityPoints.isEmpty,
                          emptyMessage: "No owner-activity samples yet.") {
                    SeriesChart(points: model.history.ownerActivityPoints, unit: "Active")
                }
            }
            .padding(.top, 8)
        }
        .font(.subheadline.weight(.semibold))
        .accessibilityIdentifier("activity-graphs")
    }

    private func pathSummary(_ peers: [ConnectorStatus.MeshPeer]) -> String {
        guard !peers.isEmpty else { return "No peer route reported" }
        let direct = peers.filter { $0.path == "direct" }.count
        let relay = peers.filter { $0.path == "relay" }.count
        let cloud = peers.filter { $0.path == "cloud" }.count
        var parts: [String] = []
        if direct > 0 { parts.append("\(direct) direct") }
        if relay > 0 { parts.append("\(relay) via neXal Relay") }
        if cloud > 0 { parts.append("\(cloud) via Cloudflare") }
        let unknown = peers.count - direct - relay - cloud
        if unknown > 0 { parts.append("\(unknown) unknown") }
        return parts.joined(separator: " · ")
    }

    private func routeLabel(_ peer: ConnectorStatus.MeshPeer) -> String {
        switch peer.path {
        case "direct": return peer.pathLabel.isEmpty ? "Direct WireGuard" : peer.pathLabel
        case "relay": return peer.relayRegion.map { "neXal Relay — \($0)" } ?? "neXal Relay"
        case "cloud": return "Cloudflare route"
        default: return "Route unavailable"
        }
    }

    private func pqLabel(_ value: String) -> String {
        switch value {
        case "protected": "Protected"
        case "negotiating": "Negotiating"
        case "rekeying": "Rotating keys"
        case "degraded": "Degraded"
        case "verification_stale": "Verification stale"
        case "failed": "Failed"
        default: "Unavailable"
        }
    }

    private func bytes(_ value: UInt64) -> String {
        ByteCountFormatter.string(fromByteCount: Int64(clamping: value), countStyle: .file)
    }

    private func status(_ tunnel: TunnelIndicator) -> some View {
        HStack(alignment: .top, spacing: 9) {
            Circle().fill(color(tunnel.severity)).frame(width: 8, height: 8).padding(.top, 5)
            VStack(alignment: .leading, spacing: 3) {
                Text(tunnel.title).font(.subheadline.weight(.medium))
                Text(tunnel.detail).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private func blocked(_ reason: String) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("Connector setup needs attention", systemImage: "exclamationmark.triangle.fill")
                .font(.headline).foregroundStyle(.orange)
            Text(reason).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
            Button("Try again") { Task { await model.refresh() } }
        }
    }

    private func step(number: String, title: String, detail: String, symbol: String) -> some View {
        HStack(alignment: .top, spacing: 10) {
            ZStack {
                Circle().fill(.tint.opacity(0.12)).frame(width: 30, height: 30)
                Text(number).font(.caption.weight(.bold)).foregroundStyle(.tint)
            }
            VStack(alignment: .leading, spacing: 2) {
                Label(title, systemImage: symbol).font(.subheadline.weight(.semibold))
                Text(detail).font(.caption).foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder private var message: some View {
        if let text = model.pairingProblem ?? model.message, !text.isEmpty {
            Divider()
            Text(text).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
        }
    }

    private var footer: some View {
        HStack {
            Button("Refresh") { Task { await model.refresh() } }.buttonStyle(.link)
            Spacer()
            Button("Quit") { NSApplication.shared.terminate(nil) }.buttonStyle(.link)
        }.font(.caption)
    }

    private func color(_ severity: IndicatorSeverity) -> Color {
        switch severity {
        case .good: .green
        case .pending: .yellow
        case .warning: .orange
        case .bad: .red
        case .inactive: .secondary.opacity(0.5)
        }
    }
}
