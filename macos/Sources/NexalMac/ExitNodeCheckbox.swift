import SwiftUI

/// The owner's explicit, reversible choice to use this computer as an exit node.
struct ExitNodeCheckbox: View {
    @EnvironmentObject private var model: AppModel
    let peer: ConnectorStatus.MeshPeer
    let storageGateway: Bool

    private var route: String? {
        storageGateway ? NetworkService.storageExitRoute : model.peerExitRoutes[peer.id]
    }

    /// Offered only when it can be used (or is in use, so it can be turned off).
    private var available: Bool {
        let selected = route != nil && model.exitRoute == route
        if selected { return true }
        guard peer.lifecycle == "connected" else { return false }
        return !storageGateway || model.availableExitRoutes.contains(NetworkService.storageExitRoute)
    }

    var body: some View {
        if available { content }
    }

    private var content: some View {
        let selected = route != nil && model.exitRoute == route
        return VStack(alignment: .leading, spacing: 2) {
            Toggle(isOn: Binding(
                get: { selected },
                set: { on in Task { await model.setPeerExitRoute(peer, storageGateway: storageGateway, enabled: on) } }
            )) {
                Text(Self.label)
            }
            .toggleStyle(.checkbox)
            // A selected offline peer can always be deselected. Ordinary peers
            // no longer require a nonexistent pre-created route to enable setup.
            .disabled(model.exitRouteBusy)
            if let status = model.exitRouteStatus[peer.id] {
                Text(status).font(.caption2).foregroundStyle(.secondary)
            } else if selected {
                Text("Your internet traffic goes through this exit node.")
                    .font(.caption2).foregroundStyle(.secondary)
            } else {
                Text("While checked, your internet traffic goes through this exit node. Uncheck to go back to normal.")
                    .font(.caption2).foregroundStyle(.secondary)
            }
        }
    }

    static let label = "Route all of my internet traffic through this exit node"
}
