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
                pairingSection
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
            if model.busy {
                // The spinner alone only said "something is happening", so every
                // operation looked identical and a stall named nothing. The phase
                // says which step is running, so a hang is attributable to it.
                HStack(spacing: 6) {
                    ProgressView().controlSize(.small)
                    if let activity = model.activity {
                        Text(activity)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .accessibilityIdentifier("connector.activity")
                    }
                }
                .accessibilityElement(children: .combine)
            }
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
            // The primary action is disabled on first launch until a connector is
            // chosen, and the reason lives in a collapsed section further down.
            // A dead button with no stated cause reads as a broken app, so the
            // requirement is named at the point of disablement. Shown only while
            // it is actually blocked.
            if model.selection == nil || !model.configurationExists {
                Text(model.selection == nil
                     ? "Choose a connector under Setup below to enable Start / Connect."
                     : "No connector configuration found yet. Complete Setup below to enable Start / Connect.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
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

    /// Phone pairing (§26). The code, the role, a live countdown, the status and a
    /// Cancel button are all present, and when pairing cannot start the reason is
    /// stated next to a button that stays visible: a dead control with no
    /// explanation reads as a broken app.
    private var pairingSection: some View {
        PanelSection(title: "Pair your phone") {
            if let pairing = model.pairing {
                IndicatorRow(state: pairing.indicator, tint: .indigo)
                if let symbol = pairing.symbol {
                    HStack {
                        Spacer(minLength: 0)
                        PairingCodeView(symbol: symbol, isLive: pairing.status.isLive)
                        Spacer(minLength: 0)
                    }
                    Text(symbol.caption)
                        .font(.caption2).foregroundStyle(.secondary)
                }
                // Recomputed from the tick the model publishes each second, so the
                // countdown is live without any view owning a timer of its own.
                Text(pairing.countdown(now: model.pairingTick))
                    .font(.caption)
                    .foregroundStyle(pairing.status.isLive ? .primary : .secondary)
                Text("Pairing id \(pairing.pairingId)")
                    .font(.caption2).foregroundStyle(.secondary).textSelection(.enabled)
                HStack {
                    Button("Cancel pairing", role: .destructive) {
                        Task { await model.cancelPairing() }
                    }
                    Button("New code") { Task { await model.startPairing() } }
                }
                Text("Scanning only claims this Mac. What it may use is approved separately on the phone; this panel grants nothing.")
                    .font(.caption2).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                Picker("This Mac's role", selection: $model.pairingRole) {
                    ForEach(PairingRole.allCases) { role in
                        Text(role.label).tag(role)
                    }
                }
                .disabled(model.pairingUnavailableReason != nil)
                Text(model.pairingRole.explanation)
                    .font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Button {
                    Task { await model.startPairing() }
                } label: {
                    Label("Show pairing code", systemImage: "qrcode")
                        .frame(maxWidth: .infinity)
                }
                .accessibilityIdentifier("show-pairing-code")
                .disabled(model.pairingUnavailableReason != nil)
                if let reason = model.pairingUnavailableReason {
                    Text(reason)
                        .font(.caption).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                CaveatDisclosure(title: "How pairing works",
                                 text: "The Go connector asks your coordinator for a short-lived pairing and renders the code. The code is drawn on screen only and is never saved: it carries a one-time secret that lets the phone claim this Mac. Codes expire in a few minutes. Pairing requires an https coordinator, which both the development and production environments now provide.")
            }
            // The connector's own explanation of a failure, kept beside the
            // controls rather than in the footer where it would scroll away.
            if let problem = model.pairingProblem {
                Divider()
                Text(problem)
                    .font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
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
                // .checkbox is the macOS idiom for an independent on/off option in a
                // form. The default switch styling reads as a mode the whole panel
                // swings between, which overstated what this does: it selects which
                // coordinator a NEW configuration is created against.
                Toggle("Use development environment", isOn: $model.developmentEnvironment)
                    .toggleStyle(.checkbox)
                    .disabled(model.status != nil || model.processOwned)
                if model.developmentEnvironment {
                    Text("Coordinator: \(CoordinatorOrigins.development). Uses a separate configuration and restricted-permission file credentials, not the Keychain, so a development identity is never confused with your real one. No public execution or external spending is enabled.")
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
                    // Three separate conditions gate this button and none of them
                    // were stated, so it read as broken rather than blocked. Named
                    // in the order the owner must satisfy them, and only while the
                    // button is actually disabled.
                    if model.selection == nil {
                        Text("Choose the Go connector in step 1 first.")
                            .font(.caption2).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    } else if model.enrollmentCode.isEmpty {
                        Text("Paste the one-use enrollment code issued by your coordinator.")
                            .font(.caption2).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    } else if !model.consent {
                        Text("Approve private-only enrollment above to continue.")
                            .font(.caption2).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
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
