import AppKit
import ServiceManagement
import SwiftUI

/// Register the default once, then respect changes made here or in System Settings.
@MainActor
final class LoginItemSettings: ObservableObject {
    @Published private(set) var startsAtLogin = false
    @Published private(set) var requiresApproval = false
    @Published private(set) var errorMessage: String?

    private static let initializedKey = "connectorLoginItemInitialized"
    private let defaults: UserDefaults
    private let status: () -> SMAppService.Status
    private let register: () throws -> Void
    private let unregister: () throws -> Void

    init(defaults: UserDefaults = .standard,
         status: @escaping () -> SMAppService.Status = { SMAppService.mainApp.status },
         register: @escaping () throws -> Void = { try SMAppService.mainApp.register() },
         unregister: @escaping () throws -> Void = { try SMAppService.mainApp.unregister() }) {
        self.defaults = defaults
        self.status = status
        self.register = register
        self.unregister = unregister
        refresh()
    }

    func configureFirstLaunch() {
        guard !defaults.bool(forKey: Self.initializedKey) else { refresh(); return }
        if status() == .enabled || status() == .requiresApproval {
            defaults.set(true, forKey: Self.initializedKey)
            refresh()
        } else {
            setStartsAtLogin(true)
        }
    }

    func setStartsAtLogin(_ enabled: Bool) {
        errorMessage = nil
        do {
            if enabled { try register() } else { try unregister() }
            defaults.set(true, forKey: Self.initializedKey)
        } catch {
            errorMessage = "Could not change startup behavior: \(error.localizedDescription)"
        }
        refresh()
    }

    func refresh() {
        let current = status()
        startsAtLogin = current == .enabled || current == .requiresApproval
        requiresApproval = current == .requiresApproval
    }
}

struct ConnectorSettingsView: View {
    @ObservedObject var loginItem: LoginItemSettings
    @EnvironmentObject private var preferences: ConnectorPreferences
    @EnvironmentObject private var model: AppModel
    @State private var selectedTab = 0

    var body: some View {
        TabView(selection: $selectedTab) {
            ScrollView {
                generalSettings.padding(20)
            }
            .tabItem { Label("General", systemImage: "gearshape") }
            .tag(0)

            ScrollView {
                if selectedTab == 1 {
                    VStack(alignment: .leading, spacing: 20) {
                        if model.isLinked {
                            GroupBox {
                                CanarySettingsView().padding(8)
                            }
                            GroupBox {
                                HoneypotSettingsView().padding(8)
                            }
                        } else {
                            Label("Connect this Mac to neXal to configure security features.", systemImage: "shield")
                                .foregroundStyle(.secondary)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(20)
                }
            }
            .tabItem { Label("Security", systemImage: "shield") }
            .tag(1)
        }
        .padding(12)
        .frame(width: 540, height: 530)
        .onAppear { loginItem.refresh() }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            loginItem.refresh()
        }
    }

    private var generalSettings: some View {
        VStack(alignment: .leading, spacing: 20) {
            GroupBox("Startup") {
                VStack(alignment: .leading, spacing: 10) {
                    Toggle("Start neXal@home at login", isOn: Binding(
                        get: { loginItem.startsAtLogin },
                        set: { loginItem.setStartsAtLogin($0) }
                    ))
                    Text("Automatically start the connector when you sign in to your Mac.")
                        .font(.caption).foregroundStyle(.secondary)
                    if loginItem.requiresApproval {
                        Text("Allow neXal@home in macOS Login Items to finish enabling startup.")
                            .font(.callout)
                        Button("Open Login Items Settings") { SMAppService.openSystemSettingsLoginItems() }
                    }
                    if let error = loginItem.errorMessage {
                        Text(error).font(.callout).foregroundStyle(.red).textSelection(.enabled)
                    }
                    Divider()
                    Toggle("Show splash screen on launch", isOn: $preferences.showSplash)
                    Text("Show the neXal logo for two seconds before continuing in the menu bar. Applies next launch.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
            }
            GroupBox("Sound & menu bar") {
                VStack(alignment: .leading, spacing: 10) {
                    Toggle("Play a sound after successful pairing", isOn: $preferences.playPairingSound)
                    Toggle("Flash the menu-bar icon when attention is needed", isOn: $preferences.flashAlerts)
                    Text("With flashing off, the icon still shows its alert color and exclamation mark.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
            }
            GroupBox("Activity") {
                VStack(alignment: .leading, spacing: 10) {
                    Picker("Graph time range", selection: $preferences.chartWindowMinutes) {
                        Text("5 minutes").tag(5)
                        Text("30 minutes").tag(30)
                    }
                    Text("How much history the connector panel's activity graphs show. Remembered across launches.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
            }
        }
        .toggleStyle(.checkbox)
    }
}
