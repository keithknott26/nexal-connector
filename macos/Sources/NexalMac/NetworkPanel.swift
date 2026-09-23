import AppKit
import SwiftUI

/// The menu bar panel.
///
/// This replaces a panel that rendered, simultaneously, on a Mac that had joined nothing:
/// activity graphs, a manual job-acceptance window with its own caveat disclosure, a
/// contribute toggle, a pause button, a connector executable picker, a coordinator text
/// field, a development-environment checkbox, a pairing role picker, and a marketplace
/// gating notice. The purpose of this app is to get a Mac onto the neXal network, so
/// before that has happened there is one control, and after it there are the other Macs.
///
/// The body is a `switch` over `NetworkScreen` and holds no logic of its own. Every
/// decision -- which screen, whether the tunnel counts as quantum-safe, whether a peer
/// has a usable address -- is made in `NetworkPresentation.swift` and unit tested, because
/// SwiftUI bodies cannot be tested and this app is edited without a Swift compiler
/// available.
struct NetworkPanel: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: PanelMetrics.sectionSpacing) {
            header
            switch model.networkScreen {
            case .needsConnector(let reason):
                connectorMissing(reason)
            case .notLinked:
                joinScreen
            case .linking(let linking):
                linkingScreen(linking)
            case .linked(let network):
                linkedScreen(network)
            }
            Spacer(minLength: 0)
            footer
        }
        .padding(PanelMetrics.padding)
        .frame(width: PanelMetrics.width)
        // One poll feeds every indicator. Five seconds rather than ten: while a code is
        // on screen the panel is waiting for the phone to claim this Mac, and a ten
        // second lag there reads as the scan not having worked.
        .task {
            while !Task.isCancelled {
                await model.refresh()
                do { try await Task.sleep(for: .seconds(5)) } catch { break }
            }
        }
    }

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: "point.3.filled.connected.trianglepath.dotted")
                .foregroundStyle(.tint)
            VStack(alignment: .leading, spacing: 0) {
                Text("neXal").font(.headline)
                Text(model.hostNameDisplay).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
        }
    }

    // MARK: - Before joining

    /// The entire pre-join UI. One button.
    private var joinScreen: some View {
        VStack(alignment: .leading, spacing: PanelMetrics.rowSpacing) {
            Text("This Mac has not joined your neXal network")
                .font(.title3.weight(.semibold))
                .fixedSize(horizontal: false, vertical: true)
            Button {
                Task { await model.startPairing() }
            } label: {
                Label("Join neXal network with QR code", systemImage: "qrcode")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .accessibilityIdentifier("join-network")

            Text("Scan the code with the neXal app on your iPhone. This Mac then joins automatically.")
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private func linkingScreen(_ linking: LinkingState) -> some View {
        VStack(alignment: .leading, spacing: PanelMetrics.rowSpacing) {
            Text("Scan with your iPhone").font(.title3.weight(.semibold))
            // PairingCodeView and PairingSymbol are the existing, tested QR path -- the
            // encoder in PairingPresentation is described as the only one in the system,
            // and adding a second here would mean two ways to render an unscannable code.
            if let symbol = model.pairing?.symbol {
                HStack {
                    Spacer(minLength: 0)
                    PairingCodeView(symbol: symbol, isLive: !linking.isExpired)
                    Spacer(minLength: 0)
                }
            }
            Text(linking.statusLine)
                .font(.caption).foregroundStyle(.secondary)
                .accessibilityIdentifier("linking-status")
            // Progress is indeterminate on purpose: the phone may claim this Mac at any
            // moment, so a bar implying a known duration would be inventing one.
            ProgressView().progressViewStyle(.linear)
            Button("Cancel") { Task { await model.cancelPairing() } }
                .buttonStyle(.link)
        }
    }

    private func connectorMissing(_ reason: String) -> some View {
        VStack(alignment: .leading, spacing: PanelMetrics.rowSpacing) {
            Label("neXal cannot start", systemImage: "exclamationmark.triangle.fill")
                .font(.headline).foregroundStyle(.orange)
            Text(reason)
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
            Button("Try again") { Task { await model.refresh() } }
        }
    }

    // MARK: - After joining

    private func linkedScreen(_ network: NetworkState) -> some View {
        VStack(alignment: .leading, spacing: PanelMetrics.sectionSpacing) {
            tunnelRow(network.tunnel)
            Divider()
            peerList(network)
        }
    }

    /// The indicator whose absence was the complaint: the panel never said whether the
    /// quantum-safe tunnel was up.
    private func tunnelRow(_ tunnel: TunnelIndicator) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Circle()
                .fill(color(for: tunnel.severity))
                .frame(width: 8, height: 8)
                .padding(.top, 5)
            VStack(alignment: .leading, spacing: PanelMetrics.tightSpacing) {
                Text(tunnel.title)
                    .font(.subheadline.weight(.medium))
                    .accessibilityIdentifier("tunnel-title")
                Text(tunnel.detail)
                    .font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer()
        }
    }

    private func peerList(_ network: NetworkState) -> some View {
        VStack(alignment: .leading, spacing: PanelMetrics.rowSpacing) {
            Text("Macs on your network").font(.subheadline.weight(.medium))

            if let note = network.emptyNote {
                Text(note).font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                ForEach(network.peers) { peer in
                    peerRow(peer)
                }
                if let note = network.unreachableNote {
                    Text(note).font(.caption2).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    private func peerRow(_ peer: NetworkPeer) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Circle()
                .fill(peer.reachability.isConnectable ? Color.green : Color.secondary.opacity(0.4))
                .frame(width: 6, height: 6)
                .padding(.top, 6)
            VStack(alignment: .leading, spacing: PanelMetrics.tightSpacing) {
                Text(peer.name.isEmpty ? peer.id : peer.name)
                    .font(.callout)
                // An address appears only when it is genuinely reachable. The connector
                // decides that; the UI never falls back to a public or observed address,
                // which would print a target that cannot be dialed.
                if let address = peer.displayAddress {
                    Text(address)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                } else {
                    Text(peer.statusNote)
                        .font(.caption2).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            Spacer()
            if peer.reachability.isConnectable {
                Menu {
                    if let ssh = peer.sshCommand(user: model.loginName) {
                        Button("Copy ssh command") { copy(ssh) }
                    }
                    if let vnc = peer.vncURL() {
                        Button("Open screen sharing") { open(vnc) }
                        Button("Copy VNC address") { copy(vnc) }
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
            }
        }
    }

    private var footer: some View {
        HStack {
            // Sharing is no longer a checkbox. Joining is the consent, so this reports
            // the state instead of asking for permission the owner already gave; the
            // connector's own default now contributes.
            Text(model.contributes ? "Sharing this Mac's spare capacity" : "Not sharing — paused")
                .font(.caption2).foregroundStyle(.secondary)
            Spacer()
            Button("Quit") { NSApplication.shared.terminate(nil) }
                .buttonStyle(.link).font(.caption2)
        }
    }

    private func color(for severity: IndicatorSeverity) -> Color {
        switch severity {
        case .good:     return .green
        case .pending:  return .yellow
        case .warning:  return .orange
        case .bad:      return .red
        case .inactive: return .secondary.opacity(0.5)
        }
    }

    private func copy(_ value: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(value, forType: .string)
    }

    private func open(_ url: String) {
        if let u = URL(string: url) { NSWorkspace.shared.open(u) }
    }
}
