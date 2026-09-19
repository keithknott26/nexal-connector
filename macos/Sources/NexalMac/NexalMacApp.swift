import SwiftUI

@main
struct NexalMacApp: App {
    @StateObject private var model = AppModel()

    var body: some Scene {
        MenuBarExtra {
            ConnectorPanel()
                .environmentObject(model)
        } label: {
            Label("Nexal", systemImage: model.contributes ? "cpu" : "pause.circle")
        }
        .menuBarExtraStyle(.window)

        Settings {
            ConnectorPanel().environmentObject(model)
        }
    }
}

private struct ConnectorPanel: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                HStack {
                    Image(systemName: "square.stack.3d.up.fill")
                        .font(.title2).foregroundStyle(.tint)
                    VStack(alignment: .leading, spacing: 3) {
                        Text("Nexal Connector").font(.headline)
                        Text("Your Mac. Your resources.").font(.caption).foregroundStyle(.secondary)
                    }
                    Spacer()
                    if model.busy { ProgressView().controlSize(.small) }
                }
                GroupBox("Connection") {
                    VStack(alignment: .leading, spacing: 8) {
                        Label(model.title, systemImage: model.contributes ? "checkmark.circle" : "pause.circle")
                        if let hostID = model.status?.hostId {
                            Text("Host: \(hostID)").font(.caption).textSelection(.enabled)
                        }
                        if let mode = model.status?.mode {
                            Text(mode).font(.caption).foregroundStyle(.secondary)
                        }
                        if let transport = model.status?.transport {
                            Text("Transport: \(transport)").font(.caption).foregroundStyle(.secondary)
                        }
                        if let telemetry = model.status?.telemetry {
                            Text(telemetry.synthetic ? "Synthetic development telemetry"
                                 : (!telemetry.known ? "Telemetry unknown — Go admission fails closed"
                                    : (telemetry.ownerActive
                                       ? (model.status?.ownerActivityOverride == true
                                          ? "Owner active; private work explicitly permitted"
                                          : "Owner active; owner priority applies")
                                       : "Owner idle")))
                                .font(.caption).foregroundStyle(.secondary)
                        }
                        if let blocker = model.status?.executionBlocker, !blocker.isEmpty {
                            Text("Waiting: \(blocker)").font(.caption).foregroundStyle(.secondary)
                        } else if model.status?.manualAcceptanceSupported == true {
                            Text("Ready for an eligible job, or currently executing one.")
                                .font(.caption).foregroundStyle(.secondary)
                        }
                        if model.status?.ownerActivityOverride == true {
                            Text("You explicitly permitted private work while active.")
                                .font(.caption)
                            if let until = model.status?.acceptJobsUntil {
                                Text("Permission ends: \(until)").font(.caption2).foregroundStyle(.secondary)
                            }
                        }
                        if model.status?.productionDispatchVerified != true {
                            Text("Production dispatch not verified").font(.caption).foregroundStyle(.secondary)
                        }
                        if let updated = model.lastUpdated {
                            Text("Checked \(updated, style: .relative) ago").font(.caption).foregroundStyle(.secondary)
                        }
                        HStack {
                            Button("Start / Connect") { Task { await model.start() } }
                                .disabled(model.selection == nil || !model.configurationExists)
                            Button("Refresh") { Task { await model.refresh() } }
                        }
                    }.frame(maxWidth: .infinity, alignment: .leading).padding(4)
                }
                GroupBox("Owner controls") {
                    VStack(alignment: .leading, spacing: 10) {
                        Button {
                            Task { await model.acceptJobsNow() }
                        } label: {
                            Label(model.manualAcceptance.buttonTitle,
                                  systemImage: model.manualAcceptance.isActive ? "checkmark.circle" : "play.fill")
                                .frame(maxWidth: .infinity)
                        }
                        .buttonStyle(.borderedProminent)
                        .accessibilityIdentifier("accept-jobs-now")
                        .disabled(model.manualAcceptanceUnavailableReason != nil || model.manualAcceptance.isActive)
                        if let reason = model.manualAcceptanceUnavailableReason {
                            Text(reason).font(.caption).foregroundStyle(.secondary)
                        }
                        if let until = model.manualAcceptance.activeUntil {
                            Text("Permitted until \(until, style: .time).")
                                .font(.caption)
                            Text("An active permission window is not extended by repeated clicks.")
                                .font(.caption).foregroundStyle(.secondary)
                        }
                        Text("No idle wait: permits zero-cost private CPU jobs while you use this Mac for ten minutes. Requires the updated local preview coordinator. Memory limits still apply. Pause stops work and removes this permission.")
                            .font(.caption).foregroundStyle(.secondary)
                        Toggle("Contribute private resources", isOn: Binding(
                            get: { model.contributes },
                            set: { enabled in Task { await model.setContribution(enabled) } }
                        )).disabled(model.status == nil)
                        Text("Opt-in permits the Go connector to apply its resource and idle policies. It does not promise that a workload is available or enabled.")
                            .font(.caption).foregroundStyle(.secondary)
                        Button("Pause and cancel work", role: .destructive) {
                            Task { await model.setContribution(false) }
                        }.disabled(model.status == nil)
                        Divider()
                        Label("Cloud / marketplace contribution is gated", systemImage: "lock.shield")
                            .font(.caption)
                        Text("Private membership is not public consent. This build has no public execution, earnings, or automatic paid fallback.")
                            .font(.caption).foregroundStyle(.secondary)
                    }.padding(4)
                }
                DisclosureGroup("Setup and connector selection") {
                    VStack(alignment: .leading, spacing: 10) {
                        Text("1. Choose the installed Go connector").font(.subheadline.weight(.medium))
                        HStack {
                            Button("Use bundled nexal") { model.chooseBundledConnector() }
                            Button("Choose installed…") { model.chooseConnector() }
                        }
                        if let selection = model.selection {
                            Text(selection.url.path).font(.caption2).foregroundStyle(.secondary).textSelection(.enabled)
                        }
                        Text("2. Join with a one-use invitation").font(.subheadline.weight(.medium))
                        Toggle("Use local development preview", isOn: $model.localPreview)
                            .disabled(model.status != nil || model.processOwned)
                        if model.localPreview {
                            Text("Local coordinator: http://127.0.0.1:8787. Uses a separate preview configuration and restricted-permission file credentials, not Keychain. No PQ tunnel or public execution is enabled.")
                                .font(.caption).foregroundStyle(.secondary)
                        } else {
                            TextField("HTTPS coordinator origin", text: $model.coordinator)
                                .disabled(model.configurationExists)
                        }
                        TextField("Name of this Mac", text: $model.hostName)
                            .disabled(model.configurationExists)
                        if model.configurationExists {
                            Text("Existing configuration is preserved. Coordinator, name, and limits are managed by the Go CLI.")
                                .font(.caption).foregroundStyle(.secondary)
                        } else {
                            Picker("Memory limit", selection: $model.memoryMiB) {
                                ForEach([256, 512, 1024, 2048, 4096, 8192], id: \.self) { value in
                                    Text("\(value) MiB").tag(value)
                                }
                            }
                            Picker("Keep for owner", selection: $model.reserveMiB) {
                                ForEach([2048, 4096, 8192, 16384], id: \.self) { value in
                                    Text("\(value) MiB").tag(value)
                                }
                            }
                        }
                        if model.showsEnrollmentConfirmation {
                            // Fixed display-only mask, never the consumed invitation or CLI input.
                            SecureField("Enrollment confirmed", text: .constant(String(repeating: "x", count: 24)))
                                .disabled(true)
                                .accessibilityHidden(true)
                            HStack {
                                Label("Enrolled", systemImage: "checkmark.circle")
                                    .font(.caption)
                                Spacer()
                                Button("Use another code") { model.useAnotherEnrollmentCode() }
                            }
                            Text("The dots indicate completed enrollment. The one-use code is not retained.")
                                .font(.caption).foregroundStyle(.secondary)
                        } else {
                            SecureField("One-use enrollment code", text: $model.enrollmentCode)
                        }
                        if !model.showsEnrollmentConfirmation {
                            Toggle("I approve private-only enrollment", isOn: $model.consent)
                            Text("Approved CPU template only in the explicit development pilot. MLX, shared storage and distributed ranks are separate gated capabilities. No folder sharing or public access is enabled by this screen.")
                                .font(.caption).foregroundStyle(.secondary)
                            Button(model.configurationExists ? "Enroll existing configuration" : "Create and enroll") {
                                Task { await model.initializeAndEnroll() }
                            }.disabled(!model.consent || model.selection == nil || model.enrollmentCode.isEmpty)
                        }
                    }.padding(.top, 10)
                }
                if let message = model.message {
                    Text(message).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                        .accessibilityLabel("Connector message: \(message)")
                }
                Divider()
                HStack {
                    Text("Native preview • macOS 14+").font(.caption2).foregroundStyle(.secondary)
                    Spacer()
                    Button("Quit") { model.quit() }.keyboardShortcut("q")
                }
                Text(model.processOwned
                     ? "Quitting stops the connector started by this app."
                     : "An independently started connector is not stopped on quit.")
                    .font(.caption2).foregroundStyle(.secondary)
            }
            .padding(20)
            .disabled(model.busy)
        }
        .frame(width: 420, height: 650)
        .task {
            while !Task.isCancelled {
                await model.refresh()
                do { try await Task.sleep(for: .seconds(10)) } catch { break }
            }
        }
    }
}
