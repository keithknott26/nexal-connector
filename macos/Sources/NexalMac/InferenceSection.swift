import AppKit
import SwiftUI

/// Runs `nexal inference preflight --json` through the connector CLI (the same
/// path every other panel feature uses: AppModel.invoke) and holds the result.
/// Nothing is installed or downloaded by this check.
@MainActor
final class InferencePreflight: ObservableObject {
    enum Phase: Equatable {
        case idle
        case checking
        case done(InferenceReport)
        case failed(String)
    }

    @Published private(set) var phase: Phase = .idle

    func run(_ model: AppModel) {
        guard phase != .checking else { return }
        phase = .checking
        Task { [self] in
            do {
                let data = try await model.inferencePreflight()
                phase = .done(try InferenceReport.decode(data))
            } catch {
                phase = .failed(error.localizedDescription)
            }
        }
    }
}

private extension InferenceTone {
    var color: Color {
        switch self {
        case .good: return .green
        case .caution: return .orange
        case .bad: return .red
        case .neutral: return .secondary
        }
    }
}

/// Runs `nexal inference install` and turns its NDJSON events into a phase the view
/// can draw. Nothing here claims success unless the connector said `done` AND exited 0.
@MainActor
final class InferenceInstaller: ObservableObject {
    enum Phase: Equatable {
        case idle
        case installing(InstallProgress)
        case cancelling(InstallProgress)
        case done(InferenceInstallSummary)
        case failed(message: String, fix: String?)
        case cancelled

        var isActive: Bool {
            switch self {
            case .installing, .cancelling: return true
            default: return false
            }
        }
    }

    @Published private(set) var phase: Phase = .idle
    private var token: ProcessCancelToken?
    private var progress = InstallProgress(modelId: "")
    private var doneSummary: InferenceInstallSummary?
    private var errorEvent: (code: String, message: String, fix: String?)?

    func start(modelId: String, model: AppModel, completion: @escaping @MainActor () -> Void) {
        guard !phase.isActive else { return }
        let token = ProcessCancelToken()
        self.token = token
        progress = InstallProgress(modelId: modelId)
        doneSummary = nil
        errorEvent = nil
        phase = .installing(progress)
        var continuation: AsyncStream<Data>.Continuation!
        let lines = AsyncStream<Data> { continuation = $0 }
        let lineSink = continuation!
        Task { [self] in
            // Lines are consumed in order on the main actor; the stream ends when the child exits.
            let run = Task<ConnectorStreamResult, Error> {
                defer { lineSink.finish() }
                return try await model.runInferenceInstall(modelId: modelId, token: token) { _ = lineSink.yield($0) }
            }
            for await line in lines { handle(line) }
            do { finish(try await run.value) }
            catch { phase = .failed(message: error.localizedDescription, fix: nil) }
            self.token = nil
            completion()
        }
    }

    /// Terminates the child process. The phase becomes `.cancelled` once it has exited.
    func cancel() {
        guard case let .installing(p) = phase else { return }
        phase = .cancelling(p)
        token?.cancel()
    }

    func dismiss() {
        if !phase.isActive { phase = .idle }
    }

    private func handle(_ line: Data) {
        guard let event = try? InferenceInstallEvent.decode(line: line) else { return }
        switch event {
        case let .done(summary): doneSummary = summary
        case let .error(code, message, fix): errorEvent = (code, message, fix)
        default: break
        }
        progress.apply(event)
        switch phase {
        case .installing: phase = .installing(progress)
        case .cancelling: phase = .cancelling(progress)
        default: break
        }
    }

    private func finish(_ result: ConnectorStreamResult) {
        if let summary = doneSummary, result.status == 0 {
            phase = .done(summary)
        } else if result.cancelled || errorEvent?.code == "cancelled" {
            phase = .cancelled
        } else if let e = errorEvent {
            phase = .failed(message: e.message, fix: e.fix)
        } else if result.timedOut {
            phase = .failed(message: "The download took too long and was stopped.", fix: nil)
        } else if result.status != 0 {
            phase = .failed(message: result.reason ?? "The install stopped (exit \(result.status)) without saying why.", fix: nil)
        } else {
            phase = .failed(message: "The install ended before it reported it was finished. Nothing is claimed as installed; check the installed list.", fix: nil)
        }
    }
}

/// The installed-models list and its Verify / Remove actions.
@MainActor
final class InstalledInferenceModels: ObservableObject {
    enum Phase: Equatable {
        case idle
        case loading
        case loaded(InferenceInstalledReport)
        case failed(String)
    }

    @Published private(set) var phase: Phase = .idle
    @Published private(set) var busy: Set<String> = []
    /// Last Verify / Remove outcome per model id.
    @Published private(set) var notes: [String: String] = [:]

    func refresh(_ model: AppModel) {
        if case .loaded = phase {} else { phase = .loading }
        Task { [self] in
            do {
                let data = try await model.inferenceStatus()
                phase = .loaded(try InferenceInstalledReport.decode(data))
            } catch { phase = .failed(error.localizedDescription) }
        }
    }

    func verify(_ id: String, model: AppModel) {
        guard !busy.contains(id) else { return }
        busy.insert(id); notes[id] = nil
        Task { [self] in
            do { notes[id] = try InferenceVerifyResult.decode(try await model.inferenceVerify(modelId: id)).message }
            catch { notes[id] = error.localizedDescription }
            busy.remove(id)
            refresh(model)
        }
    }

    func remove(_ id: String, model: AppModel) {
        guard !busy.contains(id) else { return }
        busy.insert(id); notes[id] = nil
        Task { [self] in
            do { try await model.inferenceRemove(modelId: id); notes[id] = nil }
            catch { notes[id] = error.localizedDescription }
            busy.remove(id)
            refresh(model)
        }
    }
}

/// What the remove confirmation is asking about (an installed model or a partial download).
struct PendingRemoval: Equatable {
    let id: String
    let title: String
    let message: String
}

extension InferenceInstallOffer: Identifiable {
    var id: String { modelId }
}

/// The "AI Inference" section of the network panel, beside the virtual-machine
/// section. The decoding and every sentence live in InferencePresentation and
/// InferenceInstall.swift.
struct InferenceSection: View {
    @StateObject private var check = InferencePreflight()
    @StateObject private var installer = InferenceInstaller()
    @StateObject private var installed = InstalledInferenceModels()
    @EnvironmentObject private var model: AppModel
    @State private var confirming: InferenceInstallOffer?
    @State private var pendingRemoval: PendingRemoval?

    var body: some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: 10) {
                Text(InferencePresentation.intro)
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                HStack {
                    Button { check.run(model) } label: {
                        Label(InferencePresentation.buttonTitle, systemImage: "cpu")
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .disabled(check.phase == .checking || installer.phase.isActive)
                    .accessibilityIdentifier("add-ai-inference-model")
                    Spacer()
                }
                Text(InferenceInstallText.unavailableLine)
                    .font(.caption2).foregroundStyle(.tertiary)
                installState
                switch check.phase {
                case .idle:
                    EmptyView()
                case .checking:
                    progress
                case let .failed(message):
                    Label(message, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(.orange).textSelection(.enabled)
                case let .done(report):
                    if installer.phase == .idle, let offer = InferenceInstallOffer.make(from: report) {
                        offerView(offer)
                    }
                    ReportView(presentation: InferencePresentation(report))
                }
                installedList
            }
            .padding(.top, 6)
            .task { installed.refresh(model) }
        } label: {
            Text(InferencePresentation.sectionTitle).font(.subheadline.weight(.semibold))
        }
        .sheet(item: $confirming) { offer in
            ConfirmInstallSheet(offer: offer) {
                confirming = nil
                installer.start(modelId: offer.modelId, model: model) { installed.refresh(model) }
            } cancel: { confirming = nil }
        }
        .confirmationDialog(
            "Remove \(pendingRemoval?.title ?? "model")?",
            isPresented: Binding(get: { pendingRemoval != nil }, set: { if !$0 { pendingRemoval = nil } }),
            titleVisibility: .visible, presenting: pendingRemoval
        ) { item in
            Button("Remove", role: .destructive) { installed.remove(item.id, model: model) }
            Button("Cancel", role: .cancel) {}
        } message: { item in
            Text(item.message)
        }
    }

    // MARK: Offer

    @ViewBuilder
    private func offerView(_ offer: InferenceInstallOffer) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            if let why = offer.unavailableSentence {
                // The connector's exact reason, not an error.
                Label(why, systemImage: "info.circle")
                    .font(.caption).foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
                    .accessibilityIdentifier("ai-inference-install-blocked")
            } else {
                Button(offer.buttonTitle) { confirming = offer }
                    .buttonStyle(.bordered).controlSize(.small)
                    .accessibilityIdentifier("install-ai-inference-model")
            }
        }
    }

    // MARK: Install progress and result

    @ViewBuilder
    private var installState: some View {
        switch installer.phase {
        case .idle:
            EmptyView()
        case let .installing(p), let .cancelling(p):
            let cancelling = installer.phase.isCancelling
            VStack(alignment: .leading, spacing: 6) {
                Text(cancelling ? "Stopping…" : "Installing \(p.modelId)").font(.callout.weight(.semibold))
                ProgressView(value: p.fraction)
                HStack {
                    Text("\(p.percentLabel) · \(p.sizeLabel)").font(.caption)
                    Spacer()
                    Text(p.speedLabel ?? "measuring speed…").font(.caption).foregroundStyle(.secondary)
                }
                if let file = p.currentFile {
                    Text(file).font(.system(.caption2, design: .monospaced)).foregroundStyle(.secondary)
                        .lineLimit(1).truncationMode(.middle)
                }
                if !p.verifiedFiles.isEmpty {
                    Text("\(p.verifiedFiles.count) \(p.verifiedFiles.count == 1 ? "file" : "files") verified")
                        .font(.caption2).foregroundStyle(.tertiary)
                }
                Button("Cancel download") { installer.cancel() }
                    .controlSize(.small).disabled(cancelling)
                    .accessibilityIdentifier("cancel-ai-inference-install")
            }
            .cardStyle()
        case let .done(summary):
            VStack(alignment: .leading, spacing: 6) {
                Label(summary.state.label, systemImage: "shippingbox")
                    .font(.callout.weight(.semibold))
                    .foregroundStyle(summary.state.tone.color)
                    .fixedSize(horizontal: false, vertical: true)
                if !summary.howToUse.isEmpty {
                    CopyableBlock(title: "How to use it", lines: summary.howToUse)
                }
                Button("Dismiss") { installer.dismiss() }.controlSize(.small)
            }
            .cardStyle()
        case let .failed(message, fix):
            VStack(alignment: .leading, spacing: 4) {
                Label(message, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption).foregroundStyle(.orange).textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                if let fix, !fix.isEmpty {
                    Text("Fix: " + fix).font(.caption).textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Button("Dismiss") { installer.dismiss() }.controlSize(.small)
            }
            .cardStyle()
        case .cancelled:
            VStack(alignment: .leading, spacing: 4) {
                Text("Download cancelled. Nothing was installed; a partial download is kept so it can resume, and can be removed below.").font(.caption)
                Button("Dismiss") { installer.dismiss() }.controlSize(.small)
            }
            .cardStyle()
        }
    }

    // MARK: Installed models

    @ViewBuilder
    private var installedList: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("INSTALLED MODELS").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
            switch installed.phase {
            case .idle, .loading:
                ProgressView().controlSize(.small)
            case let .failed(message):
                Text(message).font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
            case let .loaded(report):
                if report.models.isEmpty && report.incomplete.isEmpty {
                    Text("No models installed.").font(.caption).foregroundStyle(.secondary)
                }
                ForEach(report.models) { installedRow($0) }
                ForEach(report.incomplete) { partialRow($0) }
            }
        }
    }

    private func installedRow(_ item: InferenceInstalledModel) -> some View {
        let busy = installed.busy.contains(item.id)
        return VStack(alignment: .leading, spacing: 3) {
            Text(item.title).font(.callout)
            Text(item.state.label).font(.caption.weight(.medium)).foregroundStyle(item.state.tone.color)
                .fixedSize(horizontal: false, vertical: true)
            ForEach(InferenceInstallText.row(item), id: \.self) { line in
                Text(line).font(.caption2).foregroundStyle(.tertiary).fixedSize(horizontal: false, vertical: true)
            }
            if let note = installed.notes[item.id] {
                Text(note).font(.caption).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Button("Verify") { installed.verify(item.id, model: model) }
                    .controlSize(.small).disabled(busy)
                Button("Remove…", role: .destructive) { pendingRemoval = PendingRemoval(id: item.id, title: item.title, message: InferenceInstallText.removeMessage(item)) }
                    .controlSize(.small).disabled(busy)
                if busy { ProgressView().controlSize(.small) }
            }
        }
        .padding(.vertical, 3)
    }

    private func partialRow(_ item: InferenceIncompleteModel) -> some View {
        let busy = installed.busy.contains(item.id)
        return VStack(alignment: .leading, spacing: 3) {
            Text(item.modelId).font(.callout)
            Text(InferenceInstallText.partialLine(item)).font(.caption).foregroundStyle(.orange)
            if let note = installed.notes[item.id] {
                Text(note).font(.caption).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Button("Remove…", role: .destructive) {
                    pendingRemoval = PendingRemoval(id: item.id, title: item.modelId + " (partial download)",
                                                    message: InferenceInstallText.removePartialMessage(item))
                }
                .controlSize(.small).disabled(busy)
                if busy { ProgressView().controlSize(.small) }
            }
        }
        .padding(.vertical, 3)
    }

    private var progress: some View {
        HStack(alignment: .top, spacing: 10) {
            ProgressView().controlSize(.small)
            VStack(alignment: .leading, spacing: 2) {
                Text("Checking…").font(.callout.weight(.medium))
                ForEach(InferencePresentation.progressSteps, id: \.self) { step in
                    Text(step).font(.caption).foregroundStyle(.secondary)
                }
            }
        }
        .cardStyle()
    }
}

private extension InferenceInstaller.Phase {
    var isCancelling: Bool {
        if case .cancelling = self { return true }
        return false
    }
}

private extension View {
    func cardStyle() -> some View {
        padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(.quaternary.opacity(0.35)))
    }
}

/// Monospaced lines the owner can select, with a Copy button.
private struct CopyableBlock: View {
    let title: String
    let lines: [String]

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack {
                Text(title).font(.caption.weight(.semibold))
                Spacer()
                Button("Copy") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(lines.joined(separator: "\n"), forType: .string)
                }
                .controlSize(.mini)
            }
            Text(lines.joined(separator: "\n"))
                .font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .padding(6).frame(maxWidth: .infinity, alignment: .leading)
                .background(RoundedRectangle(cornerRadius: 6, style: .continuous).fill(.quaternary.opacity(0.5)))
        }
    }
}

/// States the download size, the disk used and where the files come from before anything starts.
private struct ConfirmInstallSheet: View {
    let offer: InferenceInstallOffer
    let confirm: () -> Void
    let cancel: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Install \(offer.displayName)?").font(.headline)
            Text(offer.sizeSentence).fixedSize(horizontal: false, vertical: true)
            Text(offer.sourceSentence).fixedSize(horizontal: false, vertical: true)
            Text("Installing does not run the model. After the download this app tells you exactly what state it is in.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            HStack {
                Spacer()
                Button("Cancel", role: .cancel, action: cancel).keyboardShortcut(.cancelAction)
                Button("Download and install", action: confirm).keyboardShortcut(.defaultAction)
                    .accessibilityIdentifier("confirm-ai-inference-install")
            }
        }
        .padding(20)
        .frame(width: 420)
    }
}

private struct ReportView: View {
    let presentation: InferencePresentation

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(presentation.report.headline).font(.callout.weight(.semibold))
                .fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
            if let card = presentation.recommendationCard { recommendation(card) }
            group("Your Macs") {
                ForEach(presentation.machineRows) { machine($0) }
            }
            if !presentation.linkRows.isEmpty {
                group("Links from this Mac") {
                    ForEach(presentation.linkRows) { link($0) }
                }
            }
            group("Models") {
                ForEach(presentation.modelRows) { modelRow($0) }
                Text(presentation.report.catalogNote).font(.caption2).foregroundStyle(.tertiary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            group("Sharing across Macs") {
                Text(presentation.sharingStatement).font(.caption).fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
            }
            if !presentation.blockerRows.isEmpty {
                group("What is in the way") {
                    ForEach(presentation.blockerRows) { blocker($0) }
                }
            }
            if !presentation.gapLines.isEmpty {
                DisclosureGroup("What this check could not see") {
                    VStack(alignment: .leading, spacing: 3) {
                        ForEach(presentation.gapLines, id: \.self) { line in
                            Text("• " + line).font(.caption).foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                    }
                    .padding(.top, 4)
                }
                .font(.caption)
            }
            Text(presentation.report.surface).font(.caption2).foregroundStyle(.tertiary)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private func group<Content: View>(_ title: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title.uppercased()).font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
            content()
        }
    }

    private func recommendation(_ card: InferencePresentation.RecommendationCard) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(card.title, systemImage: card.runnableNow ? "checkmark.seal.fill" : "exclamationmark.circle")
                .font(.callout.weight(.semibold))
                .foregroundStyle(card.runnableNow ? Color.green : Color.orange)
                .fixedSize(horizontal: false, vertical: true)
            Text(card.subtitle).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            VStack(alignment: .leading, spacing: 3) {
                Text("How to use it").font(.caption.weight(.semibold))
                ForEach(card.howToUse, id: \.self) { line in
                    Text(line).font(.caption).fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
                }
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(Color.accentColor.opacity(0.10)))
        .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).stroke(Color.accentColor.opacity(0.5), lineWidth: 1))
        .accessibilityIdentifier("ai-inference-recommendation")
    }

    private func machine(_ row: InferencePresentation.MachineRow) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Circle().fill(row.tone.color).frame(width: 7, height: 7).padding(.top, 5)
            VStack(alignment: .leading, spacing: 2) {
                Text(row.title).font(.callout)
                Text(row.subtitle).font(.caption).foregroundStyle(.secondary)
                Text(row.memoryLine).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                Text(row.runtimeLine).font(.caption).foregroundStyle(.secondary)
                if let disk = row.diskLine { Text(disk).font(.caption).foregroundStyle(.secondary) }
                ForEach(row.reasons, id: \.self) { reason in
                    Text(reason).font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    private func link(_ row: InferencePresentation.LinkRow) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Text(row.title).font(.callout)
                Text(row.qualityLabel).font(.caption2.weight(.semibold)).foregroundStyle(row.tone.color)
            }
            Text(row.detail).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            Text(row.transport).font(.caption2).foregroundStyle(.tertiary)
        }
    }

    private func modelRow(_ row: InferencePresentation.ModelRow) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Text(row.title).font(.callout.weight(row.isRecommended ? .semibold : .regular))
                if row.isRecommended {
                    Text("Recommended").font(.caption2.weight(.semibold))
                        .padding(.horizontal, 6).padding(.vertical, 1)
                        .background(Color.accentColor.opacity(0.2), in: Capsule())
                }
                Spacer()
                Text(row.verdictLabel).font(.caption.weight(.medium)).foregroundStyle(row.tone.color)
            }
            Text(row.summary).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            ForEach(row.detail, id: \.self) { line in
                Text(line).font(.caption2).foregroundStyle(.tertiary).fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(.vertical, 3)
        .padding(.horizontal, row.isRecommended ? 8 : 0)
        .background(row.isRecommended ? Color.accentColor.opacity(0.08) : Color.clear,
                    in: RoundedRectangle(cornerRadius: 8, style: .continuous))
    }

    private func blocker(_ row: InferencePresentation.BlockerRow) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Text(row.severityLabel.uppercased()).font(.caption2.weight(.semibold)).foregroundStyle(row.tone.color)
                Text(row.title).font(.callout)
            }
            Text(row.detail).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            Text("Fix: " + row.fix).font(.caption).fixedSize(horizontal: false, vertical: true)
            if let command = row.command {
                Text(command).font(.system(.caption2, design: .monospaced)).textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}
