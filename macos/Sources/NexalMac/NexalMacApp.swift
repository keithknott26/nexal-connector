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

/// Status first, graphs next, controls after that, setup collapsed
/// (HARDENING-PLAN §26.5). Spacing and type scale come from `PanelMetrics` so
/// they are consistent rather than chosen per section.
///
/// Every honesty string from the previous layout survives here. Where one
/// crowded a row it moved into a disclosure — none was deleted.
private struct ConnectorPanel: View {
    @EnvironmentObject var model: AppModel

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: PanelMetrics.sectionSpacing) {
                header
                statusSection
                detailSection
                graphsSection
                controlsSection
                setupSection
                footer
            }
            .padding(PanelMetrics.padding)
            .disabled(model.busy)
        }
        .frame(width: PanelMetrics.width, height: PanelMetrics.height)
        // The one poll in the app. Every indicator and every chart is fed from
        // it, so graphs cannot multiply into extra polling (§26.4).
        .task {
            while !Task.isCancelled {
                await model.refresh()
                do { try await Task.sleep(for: .seconds(10)) } catch { break }
            }
        }
    }

    private var header: some View {
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
    }

    /// The two always-visible indicators (§26.3), never merged, plus the active
    /// transport row. All three read the one capability seam, not a status field.
    private var statusSection: some View {
        PanelSection(title: "Status") {
            Label(model.title, systemImage: model.contributes ? "checkmark.circle" : "pause.circle")
                .font(.subheadline.weight(.semibold))
            Divider()
            IndicatorRow(state: model.resourceSharing.indicator, tint: .green)
            Divider()
            IndicatorRow(state: model.rdma.indicator, tint: .blue)
            Divider()
            IndicatorRow(state: model.transport.indicator, tint: .teal)
        }
    }

    /// The facts that were caption text before, in the same words.
    private var detailSection: some View {
        PanelSection(title: "Connection") {
            VStack(alignment: .leading, spacing: PanelMetrics.tightSpacing) {
                if let hostID = model.status?.hostId {
                    Text("Host: \(hostID)").font(.caption).textSelection(.enabled)
                }
                if let mode = model.status?.mode {
                    Text(mode).font(.caption).foregroundStyle(.secondary)
                }
                if let activity = ResourceSharingPresentation.ownerActivityLine(status: model.status) {
                    Text(activity).font(.caption).foregroundStyle(.secondary)
                }
                if model.status?.executionBlocker?.isEmpty != false,
                   model.status?.manualAcceptanceSupported == true {
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
                    Text("Checked \(updated, style: .relative) ago")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
            CaveatDisclosure(title: DomainEscrowPresentation.title,
                             text: DomainEscrowPresentation.text)
        }
    }

    /// §26.4. Four views over one bounded in-memory window. Nothing is
    /// persisted, and an empty series says "no data yet" rather than drawing a
    /// zero line, which would read as a measured value.
    private var graphsSection: some View {
        PanelSection(title: "Last 30 minutes") {
            ChartCard(title: "Memory approved for sharing vs kept for you",
                      caption: memoryCaption,
                      isEmpty: model.history.memoryPoints.isEmpty,
                      emptyMessage: "No data yet. No poll has returned a resource policy from the connector.") {
                SeriesChart(points: model.history.memoryPoints, unit: "MiB")
            }
            Divider()
            ChartCard(title: "Transfer throughput per transport",
                      caption: "Only figures measured on this Mac are ever shown here.",
                      isEmpty: model.history.throughputPoints.isEmpty,
                      emptyMessage: ConnectorHistory.throughputUnavailable) {
                SeriesChart(points: model.history.throughputPoints, unit: "MiB/s")
            }
            Divider()
            ChartCard(title: "Job activity",
                      caption: ConnectorHistory.jobActivityLimits,
                      isEmpty: model.history.jobActivityPoints.isEmpty,
                      emptyMessage: "No data yet. No poll has returned a connector status.") {
                SeriesChart(points: model.history.jobActivityPoints, unit: "Count")
            }
            Divider()
            ChartCard(title: "Owner active vs idle",
                      caption: ConnectorHistory.ownerActivityCaption,
                      isEmpty: model.history.ownerActivityPoints.isEmpty,
                      emptyMessage: "No data yet. No poll has reported known owner activity, and unknown is not idle.") {
                SeriesChart(points: model.history.ownerActivityPoints, unit: "Active")
            }
        }
    }

    private var memoryCaption: String {
        guard let free = model.history.latestAvailableMemoryMiB else {
            return ConnectorHistory.memoryCaption
        }
        return ConnectorHistory.memoryCaption + " Free memory last reported: \(Int(free)) MiB."
    }

    private var controlsSection: some View {
        PanelSection(title: "Owner controls") {
            HStack {
                Button("Start / Connect") { Task { await model.start() } }
                    .disabled(model.selection == nil || !model.configurationExists)
                Button("Refresh") { Task { await model.refresh() } }
            }
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
            VStack(alignment: .leading, spacing: PanelMetrics.tightSpacing) {
                if let reason = model.manualAcceptanceUnavailableReason {
                    Text(reason).font(.caption).foregroundStyle(.secondary)
                }
                if let until = model.manualAcceptance.activeUntil {
                    Text("Permitted until \(until, style: .time).")
                        .font(.caption)
                    Text("An active permission window is not extended by repeated clicks.")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
            CaveatDisclosure(title: "What \"accept jobs now\" permits",
                             text: "No idle wait: permits zero-cost private CPU jobs while you use this Mac for ten minutes. Requires the updated local preview coordinator. Memory limits still apply. Pause stops work and removes this permission.")
            Divider()
            Toggle("Contribute private resources", isOn: Binding(
                get: { model.contributes },
                set: { enabled in Task { await model.setContribution(enabled) } }
            )).disabled(model.status == nil)
            Text("Opt-in permits the Go connector to apply its resource and idle policies. It does not promise that a workload is available or enabled.")
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Button("Pause and cancel work", role: .destructive) {
                Task { await model.setContribution(false) }
            }.disabled(model.status == nil)
            VStack(alignment: .leading, spacing: PanelMetrics.tightSpacing) {
                Divider()
                Label("Cloud / marketplace contribution is gated", systemImage: "lock.shield")
                    .font(.caption)
                Text("Private membership is not public consent. This build has no public execution, earnings, or automatic paid fallback.")
                    .font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private var setupSection: some View {
        DisclosureGroup("Setup and connector selection") {
            VStack(alignment: .leading, spacing: PanelMetrics.rowSpacing) {
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
            }
            .padding(.top, PanelMetrics.rowSpacing)
        }
        .font(.subheadline)
    }

    private var footer: some View {
        VStack(alignment: .leading, spacing: PanelMetrics.tightSpacing) {
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
    }
}
