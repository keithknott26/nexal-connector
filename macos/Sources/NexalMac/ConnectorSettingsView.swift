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

            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    GroupBox("Virtual Machines & Development Containers") {
                        ThrowawayHostingSettingsView().padding(8)
                    }
                    Text("Start new ones from the neXal panel › Virtual Machines & Development Containers › New…")
                        .font(.caption).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(20)
                .toggleStyle(.checkbox)
            }
            .tabItem { Label("Virtual Machine Hosting", systemImage: "server.rack") }
            .tag(2)

            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    GroupBox("Sharing on this Mac") {
                        SharingServicesSettingsView().padding(8)
                    }
                    Text("Remote Login (SSH), Screen Sharing and File Sharing turned on here are opened automatically to your other computers and your iPhone on the private network. On the same Wi-Fi, your iPhone connects to this Mac's local address first.")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(20)
                .toggleStyle(.checkbox)
            }
            .tabItem { Label("Remote Access", systemImage: "terminal") }
            .tag(3)

            ScrollView {
                subscriptionSettings.padding(20)
            }
            .tabItem { Label("Subscription", systemImage: "creditcard") }
            .tag(4)
        }
        .padding(12)
        .frame(width: 620, height: 600)
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
            GroupBox("Distributed network tasks") {
                VStack(alignment: .leading, spacing: 6) {
                    Toggle("Accept distributed network tasks while I'm using my computer", isOn: Binding(
                        get: { model.status?.shareWhileActive == true },
                        set: { on in Task { await model.setShareWhileActive(on) } }))
                    .disabled(model.status == nil || model.busy)
                    Text("On (default): tasks from your network, including capability tests, also run while you work. Off: they run only after this Mac has been idle for a while. Memory, disk, battery and temperature limits always apply, so your own apps keep priority.")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
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
            GroupBox("Diagnostics") {
                DiagnosticsSettingsView().padding(8)
            }
        }
        .toggleStyle(.checkbox)
    }

    // MARK: - Subscription

    private var subscriptionSettings: some View {
        VStack(alignment: .leading, spacing: 20) {
            GroupBox {
                VStack(alignment: .leading, spacing: 14) {
                    HStack(spacing: 10) {
                        Image(systemName: "checkmark.seal.fill")
                            .font(.title)
                            .foregroundStyle(.blue)
                        VStack(alignment: .leading, spacing: 2) {
                            Text("neXal@home")
                                .font(.headline)
                            Text("Personal Network Membership")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                        }
                    }

                    Divider()

                    Text("Your membership includes:")
                        .font(.subheadline.weight(.medium))

                    VStack(alignment: .leading, spacing: 8) {
                        benefitRow("lock.shield", "End-to-end encrypted mesh networking with post-quantum key exchange")
                        benefitRow("desktopcomputer", "Connect unlimited personal devices — Macs, iPhones, and iPads")
                        benefitRow("network", "Private peer-to-peer connections with automatic NAT traversal")
                        benefitRow("externaldrive.connected.to.line.below", "Secure file sharing and screen sharing across your network")
                        benefitRow("server.rack", "Virtual machine and development container hosting on your own hardware")
                        benefitRow("gauge.with.dots.needle.33percent", "Bandwidth monitoring and network performance insights")
                        benefitRow("bell.badge", "Canary and honeypot intrusion detection for your network")
                        benefitRow("terminal", "Remote access — SSH, Screen Sharing, and File Sharing over your private network")
                    }

                    Divider()

                    Text("Paid membership unlocks priority relay infrastructure, expanded VM hosting quotas, advanced analytics, and dedicated network support.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
            }

            GroupBox("Manage membership") {
                VStack(alignment: .leading, spacing: 12) {
                    if model.isLinked {
                        leaveNetworkSection
                    } else {
                        Label("Connect this Mac to neXal to manage your membership.", systemImage: "link")
                            .foregroundStyle(.secondary)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
            }
        }
    }

    private func benefitRow(_ symbol: String, _ text: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: symbol)
                .frame(width: 18, alignment: .center)
                .foregroundStyle(.blue)
            Text(text)
                .font(.callout)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    @ViewBuilder
    private var leaveNetworkSection: some View {
        if model.leavePhase == .confirming {
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
        } else {
            Text("Leaving the neXal network removes this Mac from your personal mesh, revokes all keys, and disconnects all shared services. This cannot be undone.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Button("Leave neXal network", role: .destructive) { model.requestLeave() }
                .disabled(model.leavePhase?.inProgress == true)
                .accessibilityIdentifier("leave-network")
        }
    }
}
