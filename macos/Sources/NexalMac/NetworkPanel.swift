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
                Label("Connected to the neXal@home network", systemImage: "checkmark.circle.fill")
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
            LabeledContent("Tunnel") { Text("\(lifecycleEmoji(mesh.lifecycle)) \(mesh.lifecycle.capitalized)") }
            LabeledContent("Post-quantum protection") { pqText(mesh.pq) }
            LabeledContent("Network path", value: pathSummary(mesh.peers))
            if let step = mesh.authenticationStep, !step.isEmpty {
                LabeledContent("Connection step", value: step)
            }
            if let updated = mesh.updatedAt, !updated.isEmpty {
                Text("Evidence updated \(friendlyTime(updated))").font(.caption2).foregroundStyle(.secondary)
            }
        }
        .font(.caption)
        .padding(12)
        .background(.secondary.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
        .accessibilityIdentifier("mesh-summary")
    }

    private func host(_ peer: ConnectorStatus.MeshPeer) -> some View {
        let details = hostDetails(for: peer)
        let info = model.peerNetInfo[peer.id]
        let publicIP = details?.publicIp ?? info?.publicAddress
        let location = details?.location ?? info?.location
        return DisclosureGroup {
            VStack(alignment: .leading, spacing: 6) {
                quickLinks(peer)
                LabeledContent("Connection") { Text("\(lifecycleEmoji(peer.lifecycle)) \(peer.lifecycle.capitalized)") }
                exitNodeCheckbox(peer)
                LabeledContent("Post-quantum protection") { pqText(peer.pq) }
                LabeledContent("Current path") { routeText(peer) }
                if let step = peer.authenticationStep, !step.isEmpty { LabeledContent("Authentication", value: step) }
                if let latency = peer.latencyMs { LabeledContent("Latency", value: latency.formatted(.number.precision(.fractionLength(0))) + " ms") }
                if let loss = peer.packetLossPercent { LabeledContent("Packet loss", value: loss.formatted(.number.precision(.fractionLength(1))) + "%") }
                LabeledContent("Traffic", value: "↑ \(bytes(peer.traffic.sentBytes)) sent · ↓ \(bytes(peer.traffic.receivedBytes)) received")
                if let handshake = peer.lastHandshakeAt { LabeledContent("Last handshake", value: friendlyTime(handshake)) }
                if let verified = peer.pqVerifiedAt { LabeledContent("PQ last verified", value: friendlyTime(verified)) }
                if let hostname = peer.hostname?.hostname { LabeledContent("neXal address", value: hostname) }
                LabeledContent("Time Machine backup location") {
                    if isStorageGateway(peer) { Text("🕰️ Yes").foregroundStyle(.green).fontWeight(.semibold) } else { Text("No") }
                }
                Divider().padding(.vertical, 2)
                if isStorageGateway(peer) {
                    Text("Managed by neXal: encrypted storage that holds this network's Time Machine backups. Reachable only for backups (SMB), never into your Macs.")
                        .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                } else {
                    systemDetails(details)
                }
            }
            .font(.caption)
            .padding(.top, 6)
            // Line the details up with the header's text ("Quantum Safe: On"):
            // the disclosure chevron plus the status dot and its spacing.
            .padding(.leading, Self.hostDetailIndent)
        } label: {
            HStack(alignment: .top, spacing: Self.hostDotSpacing) {
                Circle().fill(peer.lifecycle == "connected" ? Color.green : Color.orange)
                    .frame(width: Self.hostDotSize, height: Self.hostDotSize).padding(.top, 5)
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 5) {
                        Text(displayName(peer)).font(.subheadline.weight(.semibold))
                        if isStorageGateway(peer) {
                            Image(systemName: "externaldrive.badge.timemachine").help("Time Machine backup location")
                        }
                        if exitRouteID(for: peer) == model.exitRoute, model.exitRoute != nil {
                            Image(systemName: "cloud.fill").foregroundStyle(.blue).help("Your internet traffic exits here")
                        } else if model.availableExitRoutes.contains(exitRouteID(for: peer)) {
                            Image(systemName: "cloud").help("Can be used as an exit node")
                        }
                    }
                    .foregroundStyle(.primary)
                    Group {
                        // neXal's own infrastructure: its addresses are not the
                        // customer's to use, so they are not shown.
                        if !isStorageGateway(peer) {
                            Text("Public IP: \(publicIP ?? "—")")
                            // The computer's own LAN address, as it reported it. Not the
                            // tunnel endpoint, which is the public address when the
                            // tunnel runs through the router (see Current path).
                            Text("Private IP: \(details?.lanAddress ?? (peer.directIsPrivate == true ? peer.directAddress : nil) ?? "—")")
                        }
                        Text("Location: \(location ?? "—")")
                        Text("Quantum Safe: ") + Text(peer.pq == "protected" ? "🔐 On" : "Off")
                            .foregroundColor(peer.pq == "protected" ? .green : .secondary)
                    }
                    .font(.caption).foregroundStyle(.secondary)
                }
                .textSelection(.enabled)
            }
        }
        .accessibilityIdentifier("host-\(peer.id)")
    }

    /// The neXal storage gateway (the Time Machine destination) among the peers:
    /// the peer whose device name is the first label of the Time Machine
    /// destination host the coordinator gave this Mac, or, before Time Machine
    /// is configured, a peer following the operator's gateway naming (gw-<region>).
    private func isStorageGateway(_ peer: ConnectorStatus.MeshPeer) -> Bool {
        let name = peer.name.lowercased()
        if let host = model.timeMachine?.timeMachine.host?.lowercased(),
           let label = host.split(separator: ".").first, !label.isEmpty {
            return name == label
        }
        return name.range(of: #"^gw-[a-z0-9-]+$"#, options: .regularExpression) != nil
    }

    private func exitRouteID(for peer: ConnectorStatus.MeshPeer) -> String {
        isStorageGateway(peer) ? NetworkService.storageExitRoute : NetworkService.exitRoute(forPeerNamed: peer.name)
    }

    /// The per-peer exit-node checkbox lives in ExitNodeCheckbox.swift: it is the
    /// one deliberate customer switch, kept out of this file so the contract test
    /// can keep forbidding every other Toggle here.
    private func exitNodeCheckbox(_ peer: ConnectorStatus.MeshPeer) -> some View {
        ExitNodeCheckbox(route: exitRouteID(for: peer), peerConnected: peer.lifecycle == "connected")
    }

    /// The computer's own name as it reported it (Computer Name in System
    /// Settings), falling back to the secure network's peer name. The peer name is
    /// fixed when the computer first registers, from its hostname at that moment,
    /// so a Mac set up with Migration Assistant can carry the old Mac's name there.
    private func displayName(_ peer: ConnectorStatus.MeshPeer) -> String {
        if isStorageGateway(peer) { return "neXal Storage" }
        if let name = hostDetails(for: peer)?.name?.trimmingCharacters(in: .whitespaces), !name.isEmpty { return name }
        return peer.name.isEmpty ? peer.id : peer.name
    }

    /// Host row geometry, shared by the header and the expanded details so they
    /// line up. The indent is macOS's disclosure chevron column (about 12 pt)
    /// plus the status dot and the gap after it.
    private static let hostDotSize: CGFloat = 7
    private static let hostDotSpacing: CGFloat = 8
    private static let hostDetailIndent: CGFloat = 12 + hostDotSize + hostDotSpacing

    /// The other computer's self-reported details, matched by tunnel address.
    private func hostDetails(for peer: ConnectorStatus.MeshPeer) -> ConnectorStatus.HostDetails? {
        guard let tunnel = peer.tunnelAddress else { return nil }
        return model.status?.presence?.hosts?.first { $0.info.tunnelAddress == tunnel }?.info
    }

    @ViewBuilder
    private func systemDetails(_ d: ConnectorStatus.HostDetails?) -> some View {
        if let d {
            if let os = d.os { LabeledContent("Operating system", value: "\(os.hasPrefix("macOS") ? "🍎 " : "🐧 ")\(os)") }
            if let model = d.model { LabeledContent("Model", value: model) }
            if let chip = d.chip { LabeledContent("Processor", value: chip) }
            if let cores = d.cores {
                let split = (d.performanceCores ?? 0) > 0 && (d.efficiencyCores ?? 0) > 0
                    ? " (\(d.performanceCores!) performance + \(d.efficiencyCores!) efficiency)" : ""
                LabeledContent("Cores", value: "\(cores)\(split)")
            }
            if let memory = d.memoryBytes { LabeledContent("Memory", value: memoryText(memory)) }
            if let total = d.diskTotalBytes {
                let free = d.diskFreeBytes.map { "\(diskText($0)) free of " } ?? ""
                LabeledContent("Disk", value: "💾 \(free)\(diskText(total))")
            }
            if let thermal = d.thermal {
                LabeledContent("Thermal") {
                    switch thermal {
                    case "nominal": Text("🌡️ Normal").foregroundStyle(.green)
                    case "throttled": Text("🔥 Throttling (hot)").foregroundStyle(.orange)
                    default: Text("Unknown")
                    }
                }
            }
            if let percent = d.batteryPercent, let state = d.batteryState {
                LabeledContent("Battery") {
                    Text("\(state == "discharging" ? (percent <= 20 ? "🪫" : "🔋") : "🔌") \(percent)% · \(batteryStateText(state))")
                        .foregroundStyle(percent <= 20 && state == "discharging" ? .red : .primary)
                }
            }
        } else {
            Text("System details have not been reported yet. They appear once that computer runs the latest neXal Connector.")
                .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
    }

    private func memoryText(_ value: UInt64) -> String {
        ByteCountFormatter.string(fromByteCount: Int64(clamping: value), countStyle: .memory)
    }
    private func diskText(_ value: UInt64) -> String {
        ByteCountFormatter.string(fromByteCount: Int64(clamping: value), countStyle: .file)
    }
    private func batteryStateText(_ state: String) -> String {
        switch state {
        case "charging": "charging"
        case "charged": "fully charged"
        case "ac": "on power adapter"
        default: "on battery"
        }
    }

    private func lifecycleEmoji(_ lifecycle: String) -> String {
        switch lifecycle {
        case "connected": "🟢"
        case "failed": "🔴"
        case "degraded": "🟠"
        default: "🟡"
        }
    }

    /// "Protected" in green with the negotiated scheme in words. The secure
    /// network adds Rosenpass to WireGuard: a post-quantum key exchange built on
    /// ML-KEM (Kyber) and Classic McEliece, mixed into WireGuard's X25519 keys.
    @ViewBuilder
    private func pqText(_ value: String) -> some View {
        HStack(spacing: 4) {
            if value == "protected" {
                Text("🔐 Protected").foregroundStyle(.green).fontWeight(.semibold)
                Text("· ML-KEM + McEliece").foregroundStyle(.secondary)
            } else {
                Text(pqLabel(value)).foregroundStyle(value == "failed" ? .red : .orange)
            }
            pqInfoButton
        }
        .lineLimit(1)
        .fixedSize()
    }

    /// Opens a plain-language explanation of post-quantum cryptography. The
    /// tooltip names the exact scheme: the secure network adds Rosenpass
    /// (ML-KEM/Kyber + Classic McEliece) on top of WireGuard's X25519 keys.
    private var pqInfoButton: some View {
        Button {
            if let url = URL(string: "https://en.wikipedia.org/wiki/Post-quantum_cryptography") {
                NSWorkspace.shared.open(url)
            }
        } label: {
            Image(systemName: "info.circle")
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.secondary)
        .help("Post-quantum key exchange: ML-KEM (Kyber) + Classic McEliece via Rosenpass, combined with WireGuard X25519. Click to learn more.")
        .accessibilityLabel("About post-quantum protection")
    }

    @ViewBuilder
    private func routeText(_ peer: ConnectorStatus.MeshPeer) -> some View {
        if peer.path == "direct" {
            HStack(spacing: 4) {
                Text("⚡️ P2P Direct").foregroundStyle(.green).fontWeight(.semibold)
                switch peer.directVia {
                case "lan": Text("· local network").foregroundStyle(.secondary)
                case "nat": Text("· via router's public address").foregroundStyle(.secondary)
                default: EmptyView()
                }
            }
            .help(peer.directAddress.map { "Tunnel endpoint: \($0)" } ?? "")
        } else {
            Text(routeLabel(peer)).foregroundStyle(peer.path == "relay" ? .orange : .secondary)
        }
    }

    /// RFC 3339 timestamp → "01:13:22 EDT · 12 seconds ago", or with the date
    /// ("Sep 24, 01:13:22 EDT") when it is not today. Raw string if unparseable.
    private func friendlyTime(_ stamp: String) -> String {
        let trimmed = stamp.replacingOccurrences(of: #"\.\d+"#, with: "", options: .regularExpression)
        guard let date = ISO8601DateFormatter().date(from: trimmed) else { return stamp }
        if date.timeIntervalSince1970 < 86_400 { return "never" }
        let out = DateFormatter()
        out.locale = Locale(identifier: "en_US_POSIX")
        out.dateFormat = Calendar.current.isDateInToday(date) ? "HH:mm:ss zzz" : "MMM d, HH:mm:ss zzz"
        let relative = RelativeDateTimeFormatter()
        relative.unitsStyle = .full
        let ago = abs(date.timeIntervalSinceNow) < 5 ? "just now" : relative.localizedString(for: date, relativeTo: Date())
        return "\(out.string(from: date)) · \(ago)"
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
        // neXal Storage is operator infrastructure: shown, but greyed out, since
        // customers cannot sign in to it or wake it.
        .disabled(isStorageGateway(peer))
        .opacity(isStorageGateway(peer) ? 0.45 : 1)
        .help(isStorageGateway(peer) ? "neXal Storage is managed by neXal and is not available for remote access." : "")
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
                ChartCard(title: "neXal network latency", caption: ConnectorHistory.networkLatencyCaption,
                          isEmpty: model.history.networkLatencyPoints.isEmpty,
                          emptyMessage: "No latency yet: no other computer is connected.") {
                    SeriesChart(points: model.history.networkLatencyPoints, unit: "ms")
                }
                ChartCard(title: "Peer latency", caption: ConnectorHistory.peerLatencyCaption,
                          isEmpty: model.history.peerLatencyPoints.isEmpty,
                          emptyMessage: "No peer latency samples yet.") {
                    SeriesChart(points: model.history.peerLatencyPoints, unit: "ms")
                }
                ChartCard(title: "Traffic in / out", caption: ConnectorHistory.trafficCaption,
                          isEmpty: model.history.trafficPoints.isEmpty,
                          emptyMessage: "No traffic samples yet (needs two polls with a connected peer).") {
                    SeriesChart(points: model.history.trafficPoints, unit: "KB/s")
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
        // Encrypted with WireGuard, but this link has no post-quantum layer: the
        // other side has not enabled Rosenpass, or it has not handshaken yet.
        case "degraded": "⚠️ Not quantum-safe (WireGuard only)"
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
