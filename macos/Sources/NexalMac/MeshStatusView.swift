import AppKit
import SwiftUI

struct MeshStatusView: View {
    let mesh: ConnectorStatus.MeshStatus

    var body: some View {
        VStack(alignment: .leading, spacing: PanelMetrics.tightSpacing) {
            Label(mesh.lifecycle == "connected" ? "Connected" : "Not connected", systemImage: mesh.pq == "protected" ? "lock.shield.fill" : "network")
                .font(.subheadline.weight(.semibold))
            if let step = mesh.authenticationStep { Text(step).font(.caption).foregroundStyle(.secondary) }
            Text(mesh.pq == "protected" ? "Quantum-safe protection active" : "Quantum-safe protection not confirmed")
                .font(.caption).foregroundStyle(mesh.pq == "protected" ? Color.green : Color.secondary)
            LabeledContent("Quantum type", value: MeshQuantumPresentation.label(peers: mesh.peers))
                .font(.caption)
            Divider()
            Label("Host watermarks and honeypot", systemImage: "shield.lefthalf.filled.badge.checkmark")
                .font(.caption.weight(.semibold))
            Text("Turn these on in Settings › Security. neXal does not scan files for malware. If you see an alert, check the Mac with your own security software.")
                .font(.caption2).foregroundStyle(.secondary)
            discoveryView
            ForEach(mesh.peers) { peer in
                Divider()
                peerView(peer)
            }
        }
    }

    private var discoveryView: some View {
        let value = DiscoveryPresentation.derive(mesh.discovery)
        return VStack(alignment: .leading, spacing: 3) {
            Label(value.title, systemImage: "dot.radiowaves.left.and.right").font(.caption.weight(.semibold))
            Text(value.detail).font(.caption2).foregroundStyle(.secondary)
        }
    }

    private func peerView(_ peer: ConnectorStatus.MeshPeer) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(peer.name.isEmpty ? peer.id : peer.name).font(.caption.weight(.semibold))
            Text(peer.pathLabel).font(.caption)
            LabeledContent("Quantum type", value: MeshQuantumPresentation.label(peers: [peer])).font(.caption)
            if let region = peer.relayRegion, peer.path == "relay" { Text("neXal Relay — \(region)").font(.caption).foregroundStyle(.secondary) }
            Text("↑ \(peer.traffic.sentBytes) B  ↓ \(peer.traffic.receivedBytes) B").font(.caption2).foregroundStyle(.secondary)
            fileSharingView(peer.fileSharing)
            screenSharingView(peer.screenSharing)
        }
    }

    @ViewBuilder private func fileSharingView(_ report: ConnectorStatus.MeshFileSharing?) -> some View {
        let value = SMBPresentation.derive(report)
        Text("File Sharing: \(label(value.state))").font(.caption.weight(.medium))
        Text(value.detail).font(.caption2).foregroundStyle(.secondary)
        if let address = value.finderAddress {
            Text(address).font(.system(.caption2, design: .monospaced)).textSelection(.enabled)
            HStack { Button("Copy Finder address") { copy(address) }; Button("Open shared files") { open(address, scheme: "smb") } }
        } else if value.state == .needsMacSharing { settingsButton("Open File Sharing Settings") }
    }

    @ViewBuilder private func screenSharingView(_ report: ConnectorStatus.MeshScreenSharing?) -> some View {
        let value = ScreenSharingPresentation.derive(report)
        Text("Screen Sharing: \(label(value.state))").font(.caption.weight(.medium))
        Text(value.detail).font(.caption2).foregroundStyle(.secondary)
        if let address = value.address {
            HStack { Button("Copy Screen Sharing address") { copy(address) }; Button("Open Screen Sharing") { open(address, scheme: "vnc") } }
        } else if value.state == .needsMacSharing { settingsButton("Open Sharing Settings") }
    }

    private func label(_ state: SMBPresentation.State) -> String { state == .available ? "Available" : state == .needsMacSharing ? "Needs setup" : "Not authorized" }
    private func label(_ state: ScreenSharingPresentation.State) -> String { state == .available ? "Available" : state == .needsMacSharing ? "Needs setup" : "Not authorized" }
    private func copy(_ value: String) { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(value, forType: .string) }
    private func open(_ value: String, scheme: String) { if let url = URL(string: value), url.scheme == scheme { NSWorkspace.shared.open(url) } }
    private func settingsButton(_ title: String) -> some View { Button(title) { NSWorkspace.shared.open(URL(string: "x-apple.systempreferences:com.apple.Sharing-Settings.extension")!) } }
}
