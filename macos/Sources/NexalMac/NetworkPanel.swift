import AppKit
import SwiftUI

/// The customer-facing connector has one job: connect this Mac to neXal.
/// Implementation defaults and account policy do not belong in this menu.
struct NetworkPanel: View {
    @EnvironmentObject var model: AppModel
    var showSettings: () -> Void = {}
    var showAbout: () -> Void = {}
    @EnvironmentObject private var preferences: ConnectorPreferences
    /// Sharing services listening on this Mac, checked when its row is opened.
    @State private var localServices: LocalServiceProbe.Result?

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    header
                    if let guest = model.guestAccess, !guest.isExpired(), let deadline = guest.deadline {
                        Label("Temporary access until \(deadline.formatted(date: .omitted, time: .shortened))", systemImage: "clock")
                            .font(.caption).foregroundStyle(.secondary)
                    }
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
                Text("neXal@home").font(.headline)
                Text(model.hostNameDisplay).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if model.busy { ProgressView().controlSize(.small) }
            Menu {
                Button("Settings…", action: showSettings)
                Button("About neXal@home", action: showAbout)
            } label: {
                Image(systemName: "gearshape")
            }
            .menuStyle(.borderlessButton)
            .fixedSize()
            .accessibilityLabel("Connector menu")
        }
    }

    private var readyToPair: some View {
        VStack(alignment: .leading, spacing: 14) {
            step(number: "1", title: "Sign in on your iPhone",
                 detail: "Open neXal@home on your iPhone (available in the Apple App Store) and sign in with Apple.", symbol: "apple.logo")
            step(number: "2", title: "Pair this Mac",
                 detail: "Show a one-time code here, then choose \u{201C}Pair a Mac\u{201D} in neXal@home and scan it.", symbol: "qrcode")
            GuestInvitationEntry()
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
            Text("In neXal@home on your iPhone, choose \u{201C}Pair a Mac\u{201D} and scan this code.")
                .font(.caption).foregroundStyle(.secondary)
            Text("Paired Macs can process your de-identified work if available.")
                .font(.caption).foregroundStyle(.secondary)
            if let symbol = model.pairing?.symbol {
                HStack { Spacer(minLength: 0); PairingCodeView(symbol: symbol, isLive: !linking.isExpired); Spacer(minLength: 0) }
            }
            if let code = model.pairing?.manualCode {
                VStack(spacing: 5) {
                    Text("Pair manually").font(.caption2.weight(.semibold)).foregroundStyle(.secondary)
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
            Divider()
            Text("Your connections").font(.subheadline.weight(.semibold))
            if let mesh = model.status?.mesh { thisMac(mesh) }
            if let peers = model.status?.mesh?.peers, !peers.isEmpty {
                ForEach(peers) { peer in host(peer) }
            } else if network.peers.isEmpty {
                Text("No other connections yet.").font(.caption).foregroundStyle(.secondary)
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
            ThrowawayHostsSection()
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

    private func host(_ peer: ConnectorStatus.MeshPeer) -> some View {
        let details = hostDetails(for: peer)
        let info = model.peerNetInfo[peer.id]
        let publicIP = details?.publicIp ?? info?.publicAddress
        let location = details?.location ?? info?.location
        let privateIP = details?.lanAddress ?? (peer.directIsPrivate == true ? peer.directAddress : nil)
        let storage = isStorageGateway(peer)
        return DisclosureGroup {
            VStack(alignment: .leading, spacing: 10) {
                detailSection("Connection") {
                    detailRow("Status") { Text("\(lifecycleEmoji(peer.lifecycle)) \(lifecycleText(peer.lifecycle))") }
                    detailRow("Path") { routeText(peer) }
                    if let step = peer.authenticationStep, !step.isEmpty { detailRow("Current step") { Text(humanized(step)) } }
                    if let loss = peer.packetLossPercent {
                        detailRow("Packet loss") { Text(loss.formatted(.number.precision(.fractionLength(1))) + "%") }
                    }
                    detailRow("Traffic") { Text("↑ \(bytes(peer.traffic.sentBytes)) sent · ↓ \(bytes(peer.traffic.receivedBytes)) received") }
                    if let hostname = peer.hostname?.hostname { detailRow("neXal address") { Text(hostname) } }
                }
                detailSection("Latency") {
                    HStack(alignment: .center, spacing: 14) {
                        // Only a live connection has a current round-trip time.
                        LatencyGauge(latencyMs: peer.lifecycle == "connected" ? peer.latencyMs : nil)
                        latencySummary(peer)
                    }
                }
                detailSection("Security") {
                    detailRow("Post-quantum protection") { pqText(effectivePQ(peer.pq, peers: [peer])) }
                    detailRow("Quantum type") { quantumType([peer]) }
                    if let verified = lastVerified(peer) {
                        detailRow("Last verified") {
                            Text(verified.relative).help(verified.exact)
                        }
                    }
                }
                // This connection's own traffic, same chart style as Activity graphs.
                let peerTraffic = windowed(model.history.trafficPoints(forPeer: peer.id))
                ChartCard(title: "Traffic in / out (KB/s)",
                          caption: "Data received from (in) and sent to (out) this connection.",
                          isEmpty: peerTraffic.isEmpty,
                          emptyMessage: "No traffic data yet for this connection.") {
                    SeriesChart(points: peerTraffic, unit: "KB/s")
                }
                detailSection("Services") {
                    advertisedServices(peer)

                }
                // A phone has no Mac services to act on and no Mac system details to show.
                if !isMobileDevice(peer) {
                    detailSection("Actions") {
                        VStack(alignment: .leading, spacing: 6) {
                            peerActions(peer)
                            exitNodeCheckbox(peer)
                        }
                    }
                    collapsibleSection("System") {
                        if storage {
                            Text("Managed by neXal: encrypted storage that holds this network's Time Machine backups. Reachable only for backups (SMB), never into your Macs.")
                                .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                        } else {
                            systemDetails(details)
                        }
                    }
                }
            }
            .font(.caption)
            .padding(.top, 6)
            // Line the details up with the header's text: the disclosure chevron
            // plus the status dot and its spacing.
            .padding(.leading, Self.hostDetailIndent)
        } label: {
            HStack(alignment: .top, spacing: Self.hostDotSpacing) {
                Circle().fill(peer.lifecycle == "connected" ? Color.green : Color.orange)
                    .frame(width: Self.hostDotSize, height: Self.hostDotSize).padding(.top, 5)
                Button {
                    Task { await model.refreshPeer(peer) }
                } label: {
                    if model.refreshingPeerID == peer.id {
                        ProgressView().controlSize(.mini).frame(width: 16, height: 16)
                    } else {
                        Image(systemName: "arrow.clockwise").frame(width: 16, height: 16)
                    }
                }
                .buttonStyle(.borderless)
                .disabled(model.busy)
                .help(model.peerRefreshedAt[peer.id].map {
                    "Refresh live status · Last refreshed \($0.formatted(date: .omitted, time: .standard))"
                } ?? "Refresh connection and quantum-safe status")
                .accessibilityLabel("Refresh \(displayName(peer)) status")
                .accessibilityIdentifier("refresh-peer-\(peer.id)")
                VStack(alignment: .leading, spacing: 2) {
                    // Line 1: name, role icons, and the two things worth a glance.
                    HStack(spacing: 5) {
                        Text(displayName(peer)).font(.subheadline.weight(.semibold))
                        if storage {
                            Image(systemName: "externaldrive.badge.timemachine").help("Time Machine backup location")
                        }
                        if exitRouteID(for: peer) == model.exitRoute, model.exitRoute != nil {
                            Image(systemName: "cloud.fill").foregroundStyle(.blue).help("Your internet traffic exits here")
                        } else if model.availableExitRoutes.contains(exitRouteID(for: peer)) {
                            Image(systemName: "cloud").help("Can be used as an exit node")
                        }
                        Text(effectivePQ(peer.pq, peers: [peer]) == "protected" ? "· 🔐" : "· Encrypted")
                            .font(.caption).foregroundStyle(peer.pq == "protected" ? .green : .orange)
                            .help(effectivePQ(peer.pq, peers: [peer]) == "protected" ? "Quantum-safe (ML-KEM-1024)" : "Encrypted. ML-KEM-1024 key exchange is not confirmed on this link.")
                        if peer.lifecycle == "connected", let latency = peer.latencyMs {
                            Text("· \(peer.path == "direct" ? "⚡️ " : "")\(latency.formatted(.number.precision(.fractionLength(0)))) ms")
                                .font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    .foregroundStyle(.primary)
                    // Line 2: where it is. neXal's own infrastructure shows no
                    // addresses: they are not the customer's to use.
                    Text((storage ? [location] : [publicIP, privateIP, location]).compactMap { $0 }.joined(separator: " · ").ifEmpty("—"))
                        .font(.caption).foregroundStyle(.secondary)
                        .help(storage ? "" : "Public IP · Private IP · Location")
                    if let error = model.peerRefreshErrors[peer.id] {
                        Text(error).font(.caption2).foregroundStyle(.orange)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .textSelection(.enabled)
            }
        }
        .accessibilityIdentifier("host-\(peer.id)")
    }

    /// Chart points inside the selected time range.
    private func windowed(_ points: [ConnectorHistory.SeriesPoint]) -> [ConnectorHistory.SeriesPoint] {
        let since = Date().addingTimeInterval(-Double(preferences.chartWindowMinutes) * 60)
        return points.filter { $0.at >= since }
    }

    /// Lowest, mean and highest latency this connection reported over the last five
    /// minutes of polls, next to its gauge. Says so when there is nothing to summarise.
    private func latencySummary(_ peer: ConnectorStatus.MeshPeer) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text("Last 5 min").foregroundStyle(.secondary)
            if let stats = model.history.latencyStats(forPeer: peer.id) {
                Text("Min \(milliseconds(stats.min))")
                Text("Avg \(milliseconds(stats.avg))")
                Text("Max \(milliseconds(stats.max))")
            } else {
                Text("No measurements yet.").foregroundStyle(.secondary)
            }
        }
        .monospacedDigit()
        .accessibilityElement(children: .combine)
    }

    private func milliseconds(_ value: Double) -> String {
        value.formatted(.number.precision(.fractionLength(0))) + " ms"
    }

    /// A titled group of aligned label/value rows inside a host's details.
    private func detailSection<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title.uppercased())
                .font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 5) {
                content()
            }
        }
    }

    /// This Mac, first in the list: its own connection, its post-quantum claim (which
    /// follows the neXal gateway link) and the services it offers to the others.
    private func thisMac(_ mesh: ConnectorStatus.MeshStatus) -> some View {
        let gateway = mesh.peers.first { isStorageGateway($0) && $0.lifecycle == "connected" }
        let protected = mesh.pq == "protected"
        return DisclosureGroup {
            VStack(alignment: .leading, spacing: 10) {
                detailSection("Connection") {
                    detailRow("Status") { Text("\(lifecycleEmoji(mesh.lifecycle)) \(lifecycleText(mesh.lifecycle))") }
                    detailRow("Path") { Text(pathSummary(mesh.peers)) }
                    if let step = mesh.authenticationStep, !step.isEmpty {
                        detailRow("Current step") { Text(humanized(step)) }
                    }
                    if let updated = mesh.updatedAt, !updated.isEmpty {
                        detailRow("Updated") { Text(friendlyTime(updated)) }
                    }
                }
                detailSection("Security") {
                    detailRow("Post-quantum protection") { pqText(effectivePQ(mesh.pq, peers: mesh.peers)) }
                    detailRow("Quantum type") { quantumType(mesh.peers) }
                    if let gateway, let verified = lastVerified(gateway) {
                        detailRow("Last verified") { Text(verified.relative).help(verified.exact) }
                    }
                    Text("Based on this Mac's link to neXal storage.")
                        .font(.caption2).foregroundStyle(.secondary)
                }
                detailSection("Services") {
                    detailRow("Remote Login (SSH)") { serviceState(localServices?.remoteLogin) }
                    detailRow("Screen Sharing") { serviceState(localServices?.screenSharing) }
                    detailRow("File Sharing") { serviceState(localServices?.fileSharing) }
                }
            }
            .font(.caption)
            .padding(.top, 6)
            .padding(.leading, Self.hostDetailIndent)
            .task { localServices = await LocalServiceProbe.run() }
        } label: {
            HStack(alignment: .top, spacing: Self.hostDotSpacing) {
                Circle().fill(mesh.lifecycle == "connected" ? Color.green : Color.orange)
                    .frame(width: Self.hostDotSize, height: Self.hostDotSize).padding(.top, 5)
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        Text("\(model.hostName) (this Mac)").font(.body.weight(.semibold))
                        if protected { Text("· 🔐").help("Quantum-safe link to neXal storage") }
                    }
                    Text(lifecycleText(mesh.lifecycle)).font(.caption).foregroundStyle(.secondary)
                }
            }
        }
        .accessibilityIdentifier("this-mac")
    }

    private func serviceState(_ on: Bool?) -> some View {
        Text(on == nil ? "Checking…" : on == true ? "On" : "Off")
            .foregroundStyle(on == true ? .primary : .secondary)
    }

    /// Like `detailSection`, but collapsed until the owner opens it.
    private func collapsibleSection<Content: View>(_ title: String, @ViewBuilder _ content: @escaping () -> Content) -> some View {
        DisclosureGroup {
            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 5) {
                content()
            }
            .padding(.top, 4)
        } label: {
            Text(title.uppercased())
                .font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
        }
    }

    /// One row: label in a fixed-width column, value aligned after it, so every
    /// value in a section starts at the same position.
    private func detailRow<Value: View>(_ label: String, @ViewBuilder _ value: () -> Value) -> some View {
        GridRow {
            Text(label).foregroundStyle(.secondary).frame(width: Self.detailLabelWidth, alignment: .leading)
            value()
        }
    }

    /// The most recent of the last handshake and the last post-quantum
    /// verification, as one relative time; both exact times on hover.
    private func lastVerified(_ peer: ConnectorStatus.MeshPeer) -> (relative: String, exact: String)? {
        let stamps = [peer.lastHandshakeAt, peer.pqVerifiedAt].compactMap { $0 }
        guard !stamps.isEmpty else { return nil }
        let parse: (String) -> Date? = { ISO8601DateFormatter().date(from: $0.replacingOccurrences(of: #"\.\d+"#, with: "", options: .regularExpression)) }
        guard let latest = stamps.compactMap(parse).max() else { return nil }
        let relative = abs(latest.timeIntervalSinceNow) < 5 ? "just now"
            : RelativeDateTimeFormatter().localizedString(for: latest, relativeTo: Date())
        var exact: [String] = []
        if let h = peer.lastHandshakeAt { exact.append("Last key exchange: \(friendlyTime(h))") }
        if let v = peer.pqVerifiedAt { exact.append("Quantum-safe confirmed: \(friendlyTime(v))") }
        return (relative, exact.joined(separator: "\n"))
    }

    /// The neXal storage gateway (the Time Machine destination) among the peers:
    /// the peer whose device name is the first label of the Time Machine
    /// destination host the coordinator gave this Mac, or, before Time Machine
    /// is configured, a peer following the operator's gateway naming (gw-<region>).
    /// iPhones and iPads that joined through the neXal@home app. They run no connector,
    /// so no host details are reported for them; their mesh name is the device name.
    private func isMobileDevice(_ peer: ConnectorStatus.MeshPeer) -> Bool {
        guard hostDetails(for: peer) == nil, !isStorageGateway(peer) else { return false }
        let name = peer.name.lowercased()
        return name.contains("iphone") || name.contains("ipad")
    }

    private func isStorageGateway(_ peer: ConnectorStatus.MeshPeer) -> Bool {
        let name = peer.name.lowercased()
        if let host = model.timeMachine?.timeMachine.host?.lowercased(),
           let label = host.split(separator: ".").first, !label.isEmpty {
            return name == label || peer.tunnelAddress == host
        }
        return name.range(of: #"^gw-[a-z0-9-]+$"#, options: .regularExpression) != nil
    }

    private func exitRouteID(for peer: ConnectorStatus.MeshPeer) -> String {
        isStorageGateway(peer) ? NetworkService.storageExitRoute : (model.peerExitRoutes[peer.id] ?? "")
    }

    /// The per-peer exit-node checkbox lives in ExitNodeCheckbox.swift: it is the
    /// one deliberate customer switch, kept out of this file so the contract test
    /// can keep forbidding every other Toggle here.
    private func exitNodeCheckbox(_ peer: ConnectorStatus.MeshPeer) -> some View {
        ExitNodeCheckbox(peer: peer, storageGateway: isStorageGateway(peer))
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
    private static let hostDetailIndent: CGFloat = 12 + hostDotSize + hostDotSpacing + 16 + hostDotSpacing
    /// Width of the label column in a host's details: fits "Time Machine location".
    private static let detailLabelWidth: CGFloat = 130

    /// The other computer's self-reported details, matched by tunnel address.
    private func hostDetails(for peer: ConnectorStatus.MeshPeer) -> ConnectorStatus.HostDetails? {
        guard let tunnel = peer.tunnelAddress else { return nil }
        return model.status?.presence?.hosts?.first { $0.info.tunnelAddress == tunnel }?.info
    }

    @ViewBuilder
    private func systemDetails(_ d: ConnectorStatus.HostDetails?) -> some View {
        if let d {
            if let os = d.os {
                detailRow("Operating system") {
                    if os.hasPrefix("macOS") {
                        HStack(spacing: 4) {
                            Image(systemName: "apple.logo").accessibilityHidden(true)
                            Text(os)
                        }
                    } else {
                        Text("🐧 \(os)")
                    }
                }
            }
            if let model = d.model { detailRow("Model") { Text(model) } }
            if let chip = d.chip { detailRow("Processor") { Text(chip) } }
            if let cores = d.cores {
                let split = (d.performanceCores ?? 0) > 0 && (d.efficiencyCores ?? 0) > 0
                    ? " (\(d.performanceCores!) performance + \(d.efficiencyCores!) efficiency)" : ""
                detailRow("Cores") { Text("\(cores)\(split)") }
            }
            if let cpu = d.cpuUsagePercent { detailRow("CPU use") { Text("\(cpu, specifier: "%.0f")%") } }
            if let one = d.loadAverage1m, let five = d.loadAverage5m, let fifteen = d.loadAverage15m {
                detailRow("Load avg · 1/5/15 min") {
                    Text("\(one, specifier: "%.2f") / \(five, specifier: "%.2f") / \(fifteen, specifier: "%.2f")")
                        .help("Average runnable system workload over 1, 5 and 15 minutes. Compare with the computer’s core count; this is not CPU percent.")
                }
            }
            if let memory = d.memoryBytes { detailRow("Memory") { Text(memoryText(memory)) } }
            if let used = d.memoryUsedBytes { detailRow("Memory in use") { Text(memoryText(used)) } }
            if let idle = d.idleSeconds { detailRow("Activity") { Text(idle < 60 ? "In use" : "Idle for \(idle / 60) min") } }
            if let backup = d.lastTimeMachineBackupAt,
               let date = ISO8601DateFormatter().date(from: backup) {
                detailRow("Last backup") { Text(date.formatted(date: .abbreviated, time: .shortened)) }
            }
            if let total = d.diskTotalBytes {
                let free = d.diskFreeBytes.map { "\(diskText($0)) free of " } ?? ""
                detailRow("Disk") { Text("💾 \(free)\(diskText(total))") }
            }
            if let exit = d.exitNodeStatus {
                detailRow("Internet routing") {
                    Text(exit == "selected" ? "Exit route selected" : exit == "not_selected" ? "Exit route not selected" : exit == "unavailable" ? "No exit route offered" : "Not reported")
                        .help("As reported by that Mac. neXal does not separately check where traffic leaves the network.")
                }
            }
            if let canary = d.canaryStatus {
                detailRow("Host watermarks") {
                    Text(canary == "watching" ? "On" : canary == "alert" ? "Change detected" : canary == "disabled" ? "Off" : "Not available")
                }
            }
            if let thermal = d.thermal {
                detailRow("Thermal") {
                    switch thermal {
                    case "nominal": Text("🌡️ Normal").foregroundStyle(.green)
                    case "throttled": Text("🔥 Throttling (hot)").foregroundStyle(.orange)
                    default: Text("Unknown")
                    }
                }
            }
            if let percent = d.batteryPercent, let state = d.batteryState {
                detailRow("Battery") {
                    Text("\(state == "discharging" ? (percent <= 20 ? "🪫" : "🔋") : "🔌") \(percent)% · \(batteryStateText(state))")
                        .foregroundStyle(percent <= 20 && state == "discharging" ? .red : .primary)
                }
            }
        } else {
            Text("System details are not available yet. They appear once that Mac runs the latest neXal@home.")
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

    /// Plain words for the connector's lifecycle values; never shows the raw code.
    private func lifecycleText(_ lifecycle: String) -> String {
        switch lifecycle {
        case "connected": "Connected"
        case "authenticating": "Signing in"
        case "provisioning": "Setting up"
        case "degraded": "Limited"
        case "failed": "Not connected"
        default: "Not available"
        }
    }

    /// A reported step name with underscores removed and a capital first letter.
    private func humanized(_ value: String) -> String {
        let words = value.replacingOccurrences(of: "_", with: " ")
        return words.prefix(1).uppercased() + words.dropFirst()
    }

    private func lifecycleEmoji(_ lifecycle: String) -> String {
        switch lifecycle {
        case "connected": "🟢"
        case "failed": "🔴"
        case "degraded": "🟠"
        default: "🟡"
        }
    }

    /// Render protection separately from the evidence-validated algorithm label.
    /// Never infer a parameter set from an enabled flag.
    @ViewBuilder
    private func pqText(_ value: String) -> some View {
        HStack(spacing: 4) {
            if value == "protected" {
                Text("Protected").foregroundStyle(.green).fontWeight(.semibold)
            } else {
                Text(pqLabel(value)).foregroundStyle(value == "failed" ? .red : .orange)
            }
        }
        .lineLimit(1)
        .fixedSize()
    }

    private func quantumType(_ peers: [ConnectorStatus.MeshPeer]) -> some View {
        HStack(alignment: .lastTextBaseline, spacing: 4) {
            Text(MeshQuantumPresentation.label(peers: peers))
            pqInfoButton
        }
        .help(quantumProfileExplanation)
    }

    private var quantumProfileExplanation: String {
        "Level 5 means the link to neXal Storage recently confirmed ML-KEM-1024 key exchange (NIST Category 5 parameters). Links between your own devices are shown separately and do not change this Mac's level. Other links are shown as Encrypted."
    }

    /// The documentation explains the pinned build profile separately from
    /// the peer's protection status and unavailable negotiated-suite evidence.
    private var pqInfoButton: some View {
        Button {
            if let url = URL(string: "https://csrc.nist.gov/pubs/fips/203/final") {
                NSWorkspace.shared.open(url)
            }
        } label: {
            Image(systemName: "info.circle")
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.secondary)
        .help(quantumProfileExplanation)
        .accessibilityLabel("About post-quantum protection")
    }

    @ViewBuilder
    private func routeText(_ peer: ConnectorStatus.MeshPeer) -> some View {
        if peer.path == "direct" {
            HStack(spacing: 4) {
                Text("⚡️ Direct").foregroundStyle(.green).fontWeight(.semibold)
                switch peer.directVia {
                case "lan": Text("· local network").foregroundStyle(.secondary)
                case "nat": Text("· via router's public address").foregroundStyle(.secondary)
                default: EmptyView()
                }
            }
            .help(peer.directAddress.map { "Direct address: \($0)" } ?? "")
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

    /// Only offer known protocols, using the private-network service evidence.
    @ViewBuilder
    private func advertisedServices(_ peer: ConnectorStatus.MeshPeer) -> some View {
        let services = Set(peer.services ?? [])
        let features = model.status?.features ?? ConnectorFeatures()
        let storage = isStorageGateway(peer)
        let ssh = !storage && features.remoteSSH && services.contains("ssh") ? peer.tunnelAddress : nil
        let vnc = !storage && features.remoteVNC
            ? (peer.screenSharing?.authorized == true && peer.screenSharing?.available == true
                ? peer.screenSharing?.address : (services.contains("vnc") ? peer.tunnelAddress : nil)) : nil
        let files = !storage && features.networkFiles
            ? (peer.fileSharing?.authorized == true && peer.fileSharing?.available == true
                ? peer.fileSharing?.address : (services.contains("smb") ? peer.tunnelAddress : nil)) : nil
        VStack(alignment: .leading, spacing: 6) {
            if let host = ssh { serviceRow("SSH", symbol: "terminal", scheme: "ssh", host: host) }
            if let host = vnc { serviceRow("VNC", symbol: "display", scheme: "vnc", host: host) }
            if let host = files {
                serviceRow("File Sharing", symbol: "folder", scheme: "smb", host: host,
                           share: peer.fileSharing?.shareName)
            }
            if storage {
                HStack(spacing: 8) {
                    Text("Time Machine backup").foregroundStyle(.secondary)
                        .lineLimit(1).fixedSize(horizontal: true, vertical: false)
                    Button("Open") { model.openServiceApplication("com.apple.MigrateAssistant") }
                        .buttonStyle(.bordered).controlSize(.small)
                        .help("Open Migration Assistant to browse available backup sources")
                }
            }
            if ssh == nil && vnc == nil && files == nil && !storage {
                Text("No services available").foregroundStyle(.secondary)
            }
        }
    }

    private func serviceRow(_ title: String, symbol: String, scheme: String, host: String,
                            share: String? = nil) -> some View {
        detailRow(title) {
            if let url = PeerServiceURL.make(scheme: scheme, host: host, user: model.loginName, share: share) {
                linkButton("Open", symbol, url.absoluteString)
            } else {
                Text("Unavailable").foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private func peerActions(_ peer: ConnectorStatus.MeshPeer) -> some View {
        let tunnel = peer.tunnelAddress
        let features = model.status?.features ?? ConnectorFeatures()
        HStack(spacing: 6) {
            if features.wakeOnLAN, tunnel != nil {
                Button {
                    Task { await model.wake(peer) }
                } label: {
                    Label("Wake up", systemImage: "power")
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                .help("Wake this Mac using another neXal Mac on the same local network")
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
            if let url = URL(string: link) {
                switch url.scheme {
                case "ssh": model.openServiceApplication("com.apple.Terminal", url: url)
                case "vnc": model.openServiceApplication("com.apple.ScreenSharing", url: url)
                default: NSWorkspace.shared.open(url)
                }
            }
        } label: {
            Label(title, systemImage: symbol)
        }
        .buttonStyle(.bordered)
        .controlSize(.small)
        .help("\(title): \(link)")
    }

    private var activityGraphs: some View {
        DisclosureGroup("Activity graphs") {
            let traffic = windowed(model.history.trafficPoints)
            VStack(alignment: .leading, spacing: 14) {
                ChartCard(title: "Total traffic in / out (KB/s)", caption: ConnectorHistory.trafficCaption,
                          isEmpty: traffic.isEmpty,
                          emptyMessage: "No traffic data yet. It appears shortly after another connection comes up.") {
                    SeriesChart(points: traffic, unit: "KB/s")
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
            Button("Allow Full Disk Access…") {
                PermissionHelpWindow.shared.showFullDiskAccess()
            }
            .help("Needed only for Time Machine")
            .accessibilityIdentifier("request-full-disk-access")
            if report.timeMachine.role == "client" && report.timeMachine.enabled {
                Button("Reveal credentials…") { Task { await model.revealTimeMachineCredentials() } }
                    .disabled(model.busy)
                    .help("Show this Mac's SMB username and password for manual Time Machine setup")
            }
			LabeledContent("Status", value: timeMachineStatus(report.timeMachine.state))
			if let name = report.timeMachine.shareName { LabeledContent("Backup destination", value: name) }
			if let cap = report.timeMachine.capacityBytes { LabeledContent("Storage limit", value: bytes(cap)) }
			if let code = report.timeMachine.detailCode {
				Text(timeMachineDetail(code)).font(.caption).foregroundStyle(.secondary)
			} else if let detail = report.timeMachine.detail {
				Text(detail).font(.caption).foregroundStyle(.secondary)
			}
			switch report.timeMachine.state {
			case "ready_to_connect":
				Text("Your backup disk is ready. Add it to Time Machine on this Mac with one click.")
					.font(.caption).foregroundStyle(.secondary)
				HStack {
					Button {
						Task { await model.setUpTimeMachine() }
					} label: {
						Label("Set up Time Machine", systemImage: "externaldrive.badge.plus")
					}
					.buttonStyle(.borderedProminent)
					.disabled(model.busy)
					.accessibilityIdentifier("time-machine-setup")
					Button("Check again") { Task { await model.updateTimeMachine(force: true) } }.disabled(model.busy)
				}
			case "connected":
				Label("Backing up to neXal", systemImage: "checkmark.circle.fill")
					.foregroundStyle(.green)
				HStack {
					Button("Open Time Machine Settings") {
						if let url = URL(string: "x-apple.systempreferences:com.apple.Time-Machine-Settings.extension") {
							NSWorkspace.shared.open(url)
						}
					}
					Button("Check again") { Task { await model.updateTimeMachine(force: true) } }.disabled(model.busy)
				}
			default:
				Button("Check again") { Task { await model.updateTimeMachine(force: true) } }.disabled(model.busy)
			}
		}
		.font(.caption)
		.padding(12)
		.background(.secondary.opacity(0.08), in: RoundedRectangle(cornerRadius: 10))
		.accessibilityIdentifier("time-machine-status")
	}

	private func timeMachineStatus(_ state: String) -> String {
		switch state {
		case "ready_to_connect": return "Ready to set up"
		case "connected": return "Connected"
		case "blocked": return "Not reachable yet"
		case "disabled": return "Off"
		default: return "Needs attention"
		}
	}

	private func timeMachineDetail(_ code: String) -> String {
		switch code {
		case "juicefs_metadata_unconfigured": return "Backup storage is not ready for this Mac yet."
		case "paid_entitlement_required": return "This feature requires an eligible subscription."
		case "administrator_approval_required": return "Administrator approval is needed to add the backup destination."
		case "bonjour_advertisement_missing": return "The backup destination is running but cannot be found on the network right now."
		default: return "The backup destination needs attention. Check again in a few minutes."
		}
	}

    private func pathSummary(_ peers: [ConnectorStatus.MeshPeer]) -> String {
        guard !peers.isEmpty else { return "Waiting for another Mac" }
        let direct = peers.filter { $0.path == "direct" }.count
        let relay = peers.filter { $0.path == "relay" }.count
        let cloud = peers.filter { $0.path == "cloud" }.count
        var parts: [String] = []
        if direct > 0 { parts.append("\(direct) direct") }
        if relay > 0 { parts.append("\(relay) via neXal Relay") }
        if cloud > 0 { parts.append("\(cloud) via neXal cloud") }
        let unknown = peers.count - direct - relay - cloud
        if unknown > 0 { parts.append("\(unknown) not reported") }
        return parts.joined(separator: " · ")
    }

    private func routeLabel(_ peer: ConnectorStatus.MeshPeer) -> String {
        switch peer.path {
        case "direct": return peer.pathLabel.isEmpty ? "Direct" : peer.pathLabel
        case "relay": return peer.relayRegion.map { "neXal Relay — \($0) · metered" } ?? "neXal Relay · metered"
        case "cloud": return "neXal cloud route"
        default: return "Route unavailable"
        }
    }

    /// The protection shown next to "Quantum type" must agree with it: fresh ML-KEM-1024
    /// evidence (the same test that labels the quantum type) means protected, whatever
    /// the runtime's coarser flag says.
    private func effectivePQ(_ value: String, peers: [ConnectorStatus.MeshPeer]) -> String {
        MeshQuantumPresentation.label(peers: peers) == "Not reported" ? value : "protected"
    }

    private func pqLabel(_ value: String) -> String {
        switch value {
        case "protected": "Protected"
        case "negotiating": "Negotiating"
        case "rekeying": "Rotating keys"
        // Encrypted with WireGuard, but this link has no post-quantum layer: the
        // other side has not enabled Rosenpass, or it has not handshaken yet.
        case "degraded": "Encrypted"
        case "verification_stale": "Needs recheck"
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
            Label("Setup needs attention", systemImage: "exclamationmark.triangle.fill")
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
                Label("This Mac's saved settings point to a neXal server this version does not use (\(stale)). Remove config.json, then pair again.",
                      systemImage: "exclamationmark.triangle")
                    .foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            } else if let origin = model.configuredCoordinator {
                Text("Server: \(URL(string: origin)?.host ?? origin)").foregroundStyle(.secondary)
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

private extension String {
    /// The string, or `fallback` when it is empty.
    func ifEmpty(_ fallback: String) -> String { isEmpty ? fallback : self }
}
