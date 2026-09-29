import SwiftUI

/// PRE-PRODUCTION ONLY: choose which hosted coordinator `nexal init` writes into
/// a new config. Kept out of NetworkPanel.swift on purpose, because
/// ConnectorUIContractTests forbids controls in the customer panel source.
/// Delete this file, and its one use in NetworkPanel, when production ships.
///
/// Once config.json exists its origin is fixed (every later command reads it),
/// so the menu locks and shows the saved value instead.
struct CoordinatorChoice: View {
    @EnvironmentObject var model: AppModel

    private static let known = [CoordinatorOrigins.development, CoordinatorOrigins.production]

    var body: some View {
        Picker("Server", selection: $model.coordinator) {
            Text("Development").tag(CoordinatorOrigins.development)
            Text("Production").tag(CoordinatorOrigins.production)
            if let saved = model.configuredCoordinator, !Self.known.contains(saved) {
                Text(URL(string: saved)?.host ?? saved).tag(saved)
            }
        }
        .pickerStyle(.menu)
        .disabled(model.busy || model.configurationExists)
        .help(model.configurationExists
              ? "Set by this Mac's saved settings. Remove config.json to choose again."
              : "Which neXal server this Mac pairs with.")
        .onAppear { if let saved = model.configuredCoordinator { model.coordinator = saved } }
        .accessibilityIdentifier("coordinator-picker")
    }
}
