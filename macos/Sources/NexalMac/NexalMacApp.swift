import SwiftUI

@main
struct NexalMacApp: App {
    @StateObject private var model = AppModel()

    var body: some Scene {
        MenuBarExtra {
            NetworkPanel().environmentObject(model)
        } label: {
            Label("neXal", systemImage: model.menuBarSymbol)
        }
        .menuBarExtraStyle(.window)
    }
}
