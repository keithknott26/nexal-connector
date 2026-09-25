import AppKit
import SwiftUI

/// The customer-facing connector has one job: connect this Mac to neXal.
/// Implementation defaults and account policy do not belong in this menu.
struct NetworkPanel: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    header
                    leaveBanner.id("leave-banner")
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
            // The Leave button sits at the bottom of a long panel; bring the progress
            // and result into view instead of changing something off screen.
            .onChange(of: model.leavePhase) { _, phase in
                guard let phase, phase != .confirming else { return }
                withAnimation { proxy.scrollTo("leave-banner", anchor: .top) }
            }
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
                 detail: "Open neXal@home on your iPhone (available in the Apple App Store) and sign in with Apple.", symbol: "apple.logo")
            step(number: "2", title: "Pair this Mac",
                 detail: "Show a one-time code here, then choose \u{201C}Pair a computer\u{201D} in neXal@home and scan it.", symbol: "qrcode")
            CoordinatorChoice()
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
            } else if let problem = model.pairingProblem, !problem.isEmpty {
                // Directly under the button: a failed mint used to show only in the
                // footer, so the click looked like it did nothing.
                Label("Could not show a pairing code. \(problem)", systemImage: "exclamationmark.triangle.fill")
                    .font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .accessibilityIdentifier("pairing-problem")
            }
        }
    }

    private func pairing(_ linking: LinkingState) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Scan to connect this Mac").font(.title3.weight(.semibold))
            Text("In the neXal@home iOS application (available in the Apple App Store), choose \u{201C}Pair a computer\u{201D} and scan this QR code.")
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
            if model.status?.credentialRejected == true {
                // The tunnel can still look up while the coordinator has already
                // dropped this Mac, so this outranks the green "connected" line.
                VStack(alignment: .leading, spacing: 6) {
                    Label("This Mac needs to be paired again", systemImage: "exclamationmark.triangle.fill")
                        .font(.title3.weight(.semibold)).foregroundStyle(.orange)
                    Text("neXal no longer accepts this Mac's credential. It was removed from the network or its pairing expired. Leave the network below, then pair it again from your iPhone.")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                .accessibilityIdentifier("credential-rejected")
            } else {
                Label("This Mac is connected", systemImage: "checkmark.circle.fill")
                    .font(.title3.weight(.semibold)).foregroundStyle(.green)
            }
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
			if let report = model.timeMachine, report.timeMachine.enabled {
				Divider()
				timeMachine(report)
			}
            if model.hasUnfinishedEnrollment {
                Divider()
                Button("Clear failed pairing and start over", role: .destructive) {
                    Task { await model.resetUnfinishedPairing() }
                }.disabled(model.busy)
            }
            Divider()
            if model.leavePhase == .confirming {
                leaveConfirmation
            } else {
                Button("Leave neXal network", role: .destructive) { model.requestLeave() }
                    .disabled(model.leavePhase?.inProgress == true)
                    .accessibilityIdentifier("leave-network")
            }
        }
    }

    /// Inline instead of a dialog: MenuBarExtra windows do not reliably present one.
    private var leaveConfirmation: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label(LeavePhase.confirming.title, systemImage: LeavePhase.confirming.symbol)
                .font(.subheadline.weight(.semibold))
            Text(LeavePhase.confirming.detail)
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("Cancel", role: .cancel) { model.cancelLeave() }
                Spacer()
                Button("Leave network", role: .destructive) { Task { await model.leaveNetwork() } }
                    .buttonStyle(.borderedProminent).tint(.red)
                    .accessibilityIdentifier("confirm-leave-network")
            }
        }
        .padding(12)
        .background(Color.red.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
    }

    /// Progress and outcome of leaving, at the top of the panel. Stays visible
    /// above the new pairing code after a successful leave.
    @ViewBuilder private var leaveBanner: some View {
        if let phase = model.leavePhase, phase != .confirming,
           !(phase == .left && model.isLinked) {
            HStack(alignment: .top, spacing: 10) {
                if phase.inProgress {
                    ProgressView().controlSize(.small).padding(.top, 2)
                } else {
                    Image(systemName: phase.symbol).foregroundStyle(color(phase.severity))
                }
                VStack(alignment: .leading, spacing: 3) {
                    Text(phase.title).font(.subheadline.weight(.semibold))
                    Text(phase.detail).font(.caption).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
                }
                Spacer(minLength: 0)
                if !phase.inProgress {
                    Button { model.dismissLeaveNotice() } label: { Image(systemName: "xmark") }
                        .buttonStyle(.borderless).accessibilityLabel("Dismiss")
                }
            }
            .padding(12)
            .background(color(phase.severity).opacity(0.10), in: RoundedRectangle(cornerRadius: 10))
            .accessibilityIdentifier("leave-status")
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
                Text("Evidence updated \(clockTime(updated))").font(.caption2).foregroundStyle(.secondary)
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
                quickLinks(peer)
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
                VStack(alignment: .leading, spacing: 2) {
                    let info = model.peerNetInfo[peer.id]
                    HStack(spacing: 6) {
                        Text(peer.name.isEmpty ? peer.id : peer.name)
                        Text("Public IP: \(info?.publicAddress ?? "—")")
                            .font(.caption).foregroundStyle(.secondary)
                    }
                    Text("Direct IP: \(peer.directAddress ?? "—")   Location: \(info?.location ?? "—")")
                        .font(.caption).foregroundStyle(.secondary)
                    Text("Quantum Safe: \(peer.pq == "protected" ? "On" : "Off")")
                        .font(.caption).foregroundStyle(.secondary)
                }
                .textSelection(.enabled)
            }
        }
        .accessibilityIdentifier("host-\(peer.id)")
    }

    /// RFC 3339 timestamp (any fractional precision) → local "HH:MM:SS TZ",
    /// e.g. "03:18:59 EDT". Falls back to the raw string if it cannot parse.
    private func clockTime(_ stamp: String) -> String {
        let trimmed = stamp.replacingOccurrences(of: #"\.\d+"#, with: "", options: .regularExpression)
        guard let date = ISO8601DateFormatter().date(from: trimmed) else { return stamp }
        let out = DateFormatter()
        out.locale = Locale(identifier: "en_US_POSIX")
        out.dateFormat = "HH:mm:ss zzz"
        return out.string(from: date)
    }

    /// SSH, VNC and file-sharing links for services the peer offers. An
    /// advertised neXal address wins; otherwise the tunnel address is used.
    @ViewBuilder
    private func quickLinks(_ peer: ConnectorStatus.MeshPeer) -> some View {
        let services = Set(peer.services ?? [])
        let tunnel = peer.tunnelAddress
        HStack(spacing: 6) {
            if services.contains("ssh"), let host = tunnel {
                linkButton("SSH", "terminal", "ssh://\(host)")
            }
            if let host = (peer.screenSharing?.available == true ? peer.screenSharing?.address : nil)
                ?? (services.contains("vnc") ? tunnel : nil) {
                linkButton("VNC", "display", "vnc://\(host)")
            }
            if let host = (peer.fileSharing?.available == true ? peer.fileSharing?.address : nil)
                ?? (services.contains("smb") ? tunnel : nil) {
                linkButton("Files", "folder", "smb://\(host)")
            }
            if tunnel != nil {
                Button {
                    Task { await model.wake(peer) }
                } label: {
                    Label("Wake up", systemImage: "power")
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .help("Send a Wake-on-LAN packet through a neXal Mac on its network")
            }
        }
        if let note = model.wakeStatus[peer.id] {
            Text(note).font(.caption2).foregroundStyle(.secondary)
        }
    }

    private func linkButton(_ title: String, _ symbol: String, _ link: String) -> some View {
        Button {
            if let url = URL(string: link) { NSWorkspace.shared.open(url) }
        } label: {
            Label(title, systemImage: symbol)
        }
        .buttonStyle(.bordered)
        .controlSize(.small)
        .help("\(title): \(link)")
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

	private func timeMachine(_ report: TimeMachineReport) -> some View {
		VStack(alignment: .leading, spacing: 7) {
			Label("Time Machine", systemImage: "externaldrive.badge.timemachine")
				.font(.subheadline.weight(.semibold))
			LabeledContent("Status", value: report.timeMachine.state.replacingOccurrences(of: "_", with: " ").capitalized)
			if let name = report.timeMachine.shareName { LabeledContent("Backup destination", value: name) }
			if let cap = report.timeMachine.capacityBytes { LabeledContent("Storage limit", value: bytes(cap)) }
			if let code = report.timeMachine.detailCode {
				Text(timeMachineDetail(code)).font(.caption).foregroundStyle(.secondary)
			}
			if let action = report.action { Text(action).font(.caption2).foregroundStyle(.secondary) }
			Button("Check again") { Task { await model.updateTimeMachine(force: true) } }.disabled(model.busy)
		}
		.font(.caption)
		.padding(12)
		.background(.secondary.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
		.accessibilityIdentifier("time-machine-status")
	}

	private func timeMachineDetail(_ code: String) -> String {
		switch code {
		case "juicefs_metadata_unconfigured": return "Cloud backup storage is not ready on this computer yet. No storage credential was downloaded."
		case "paid_entitlement_required": return "This feature requires an eligible subscription."
		case "administrator_approval_required": return "Administrator approval is required to publish the backup destination."
		case "bonjour_advertisement_missing": return "The backup share is running but is not currently discoverable."
		default: return "The backup destination needs attention (\(code))."
		}
	}

    private func pathSummary(_ peers: [ConnectorStatus.MeshPeer]) -> String {
        guard !peers.isEmpty else { return "Waiting for another computer" }
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
        case "direct": return peer.pathLabel.isEmpty ? "P2P — direct" : peer.pathLabel
        case "relay": return peer.relayRegion.map { "neXal Relay — \($0) · metered" } ?? "neXal Relay · metered"
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
        if model.needsNetworkService {
            Button { Task { await model.installNetworkService() } } label: {
                Label("Install secure networking service", systemImage: "lock.shield").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .disabled(model.busy)
            .accessibilityIdentifier("install-network-service")
            Text("Asks for your Mac password once. Required for this Mac to join your network.")
                .font(.caption).foregroundStyle(.secondary)
        }
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let stale = model.coordinatorMismatch {
                Label("Saved configuration points at \(stale), which this build does not use. Remove config.json and pair again.",
                      systemImage: "exclamationmark.triangle")
                    .foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            } else if let origin = model.configuredCoordinator {
                Text("Coordinator: \(URL(string: origin)?.host ?? origin)").foregroundStyle(.secondary)
            }
            footerButtons
        }.font(.caption)
    }

    private var footerButtons: some View {
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
