import SwiftUI

/// "Route all of my internet traffic through this exit node", for one peer.
///
/// The only switch in the customer panel, and deliberately so: it is an owner
/// choice (where this Mac's internet traffic leaves), not an implementation
/// default. ConnectorUIContractTests forbids any other Toggle in NetworkPanel
/// and checks that this file holds exactly this one.
///
/// One exit node at a time: ticking one peer moves the route off any other.
/// Disabled until the coordinator has offered this peer as an exit node to this
/// Mac, and while the peer is not connected.
struct ExitNodeCheckbox: View {
    @EnvironmentObject private var model: AppModel
    let route: String
    let peerConnected: Bool

    var body: some View {
        let offered = model.availableExitRoutes.contains(route)
        VStack(alignment: .leading, spacing: 2) {
            Toggle(isOn: Binding(
                get: { model.exitRoute == route },
                set: { on in Task { await model.setExitRoute(on ? route : nil) } }
            )) {
                Text(Self.label)
            }
            .toggleStyle(.checkbox)
            .disabled(!offered || model.exitRouteBusy || !peerConnected)
            if !offered {
                Text("Not available as an exit node on your network yet.")
                    .font(.caption2).foregroundStyle(.secondary)
            } else if model.exitRoute == route {
                Text("☁️ Websites now see this exit node's address and location instead of yours.")
                    .font(.caption2).foregroundStyle(.secondary)
            }
        }
    }

    static let label = "Route all of my internet traffic through this exit node"
}
