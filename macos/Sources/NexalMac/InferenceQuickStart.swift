import SwiftUI

/// One-click AI inference: pick a model, and the app sets up the MLX runtime, pins and downloads the model
/// (every file hash-checked by the connector) and runs a local OpenAI-compatible chat server on this Mac.
/// All work happens in `nexal inference setup | pin --latest | install | serve`; this file only sequences
/// them and shows progress. The server listens on 127.0.0.1 only.
struct QuickStartModel: Identifiable, Equatable {
    let id: String
    let title: String
    let detail: String
}

@MainActor
final class InferenceQuickStart: ObservableObject {
    static let port = 8080
    static let models: [QuickStartModel] = [
        .init(id: "qwen3-4b-q4", title: "Qwen3 4B", detail: "2.3 GB download · quick, runs on any Mac"),
        .init(id: "qwen3-8b-q4", title: "Qwen3 8B", detail: "4.6 GB download · needs about 8 GB free memory"),
        .init(id: "qwen3-14b-q4", title: "Qwen3 14B", detail: "8.3 GB download · needs about 16 GB free memory"),
        .init(id: "phi-4-mini-q4", title: "Phi-4 mini", detail: "2.1 GB download · small and fast"),
    ]

    enum Phase: Equatable {
        case idle
        case working(model: String, step: String, fraction: Double?)
        case serving(model: String)
        case failed(String)
    }

    @Published private(set) var phase: Phase = .idle
    private var token: ProcessCancelToken?
    private var stopping = false

    var isBusy: Bool {
        switch phase {
        case .working, .serving: return true
        default: return false
        }
    }

    func dismiss() { if case .failed = phase { phase = .idle } }

    /// Stops whatever is running (a download or the chat server).
    func stop() {
        stopping = true
        token?.cancel()
    }

    func start(_ id: String, model: AppModel, installedIDs: Set<String>, completion: @escaping @MainActor () -> Void) {
        guard !isBusy, InferenceModelID.isValid(id) else { return }
        stopping = false
        Task { [self] in
            defer { token = nil; completion() }
            do {
                phase = .working(model: id, step: "Setting up the AI runtime (first time only, about 200 MB)…", fraction: nil)
                try await step(.inferenceSetup, model: model) { [self] line in
                    if let text = Self.message(of: line) { phase = .working(model: id, step: text + "…", fraction: nil) }
                }
                if stopping { phase = .idle; return }

                if !installedIDs.contains(id) {
                    phase = .working(model: id, step: "Checking the model files (this reads every file once)…", fraction: nil)
                    try await step(.inferencePinLatest(modelId: id), model: model) { _ in }
                    if stopping { phase = .idle; return }

                    var progress = InstallProgress(modelId: id)
                    var failure: (code: String, message: String)?
                    phase = .working(model: id, step: "Downloading the model…", fraction: nil)
                    let result = try await stream(.inferenceInstall(modelId: id), model: model) { [self] line in
                        guard let event = try? InferenceInstallEvent.decode(line: line) else { return }
                        if case let .error(code, message, _) = event { failure = (code, message) }
                        progress.apply(event)
                        phase = .working(model: id, step: "Downloading \(id): \(progress.percentLabel) · \(progress.sizeLabel)", fraction: progress.fraction)
                    }
                    if stopping { phase = .idle; return }
                    if result.status != 0 && failure?.code != "already_installed" {
                        throw QuickStartError(failure?.message ?? result.reason ?? "The download stopped without saying why.")
                    }
                }

                phase = .serving(model: id)
                let result = try await stream(.inferenceServe(modelId: id, port: Self.port), model: model) { _ in }
                if stopping { phase = .idle; return }
                phase = .failed(result.reason ?? "The model server stopped. Check that this Mac has enough free memory for \(id).")
            } catch {
                phase = stopping ? .idle : .failed(error.localizedDescription)
            }
        }
    }

    // MARK: Plumbing

    private struct QuickStartError: LocalizedError {
        let errorDescription: String?
        init(_ message: String) { errorDescription = message }
    }

    /// Reads the `message` of a `{"event":"step",...}` line.
    nonisolated static func message(of line: Data) -> String? {
        struct Line: Decodable { let event: String; let message: String? }
        guard let l = try? JSONDecoder().decode(Line.self, from: line), l.event == "step" else { return nil }
        return l.message
    }

    /// Runs one command to completion and throws unless it exited 0.
    private func step(_ command: CLICommand, model: AppModel, onLine: @escaping @MainActor (Data) -> Void) async throws {
        var errorMessage: String?
        let result = try await stream(command, model: model) { line in
            struct Err: Decodable { let event: String; let message: String? }
            if let e = try? JSONDecoder().decode(Err.self, from: line), e.event == "error" { errorMessage = e.message }
            onLine(line)
        }
        if result.status != 0 && !stopping {
            throw QuickStartError(errorMessage ?? result.reason ?? "A setup step failed (exit \(result.status)).")
        }
    }

    /// Streams a command's output lines onto the main actor, in order.
    private func stream(_ command: CLICommand, model: AppModel, onLine: @escaping @MainActor (Data) -> Void) async throws -> ConnectorStreamResult {
        let token = ProcessCancelToken()
        self.token = token
        var continuation: AsyncStream<Data>.Continuation!
        let lines = AsyncStream<Data> { continuation = $0 }
        let sink = continuation!
        let run = Task<ConnectorStreamResult, Error> {
            defer { sink.finish() }
            return try await model.runInferenceCommand(command, token: token) { _ = sink.yield($0) }
        }
        for await line in lines { onLine(line) }
        return try await run.value
    }
}

struct InferenceQuickStartView: View {
    @StateObject private var quick = InferenceQuickStart()
    @EnvironmentObject private var model: AppModel
    let installedIDs: Set<String>
    let refresh: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("RUN A MODEL ON THIS MAC").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
            switch quick.phase {
            case let .working(id, step, fraction):
                VStack(alignment: .leading, spacing: 6) {
                    Text(id).font(.callout.weight(.semibold))
                    if let fraction { ProgressView(value: fraction) } else { ProgressView().progressViewStyle(.linear) }
                    Text(step).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    Button("Cancel") { quick.stop() }.controlSize(.small)
                }
                .cardStyle()
            case let .serving(id):
                VStack(alignment: .leading, spacing: 6) {
                    Label("\(id) is running", systemImage: "bolt.horizontal.circle.fill").font(.callout.weight(.semibold)).foregroundStyle(.green)
                    CopyableBlock(title: "Chat endpoint (OpenAI-compatible, this Mac only)", lines: [
                        "http://127.0.0.1:\(InferenceQuickStart.port)/v1/chat/completions",
                    ])
                    Text("The first answer can take a minute while the model loads into memory.").font(.caption2).foregroundStyle(.tertiary)
                    Button("Stop") { quick.stop() }.controlSize(.small)
                }
                .cardStyle()
            case let .failed(message):
                VStack(alignment: .leading, spacing: 4) {
                    Label(message, systemImage: "exclamationmark.triangle.fill").font(.caption).foregroundStyle(.orange)
                        .textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
                    Button("Dismiss") { quick.dismiss() }.controlSize(.small)
                }
                .cardStyle()
            case .idle:
                EmptyView()
            }
            ForEach(InferenceQuickStart.models) { item in
                HStack {
                    VStack(alignment: .leading, spacing: 1) {
                        Text(item.title).font(.callout)
                        Text(installedIDs.contains(item.id) ? "Downloaded · " + item.detail : item.detail)
                            .font(.caption2).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button(installedIDs.contains(item.id) ? "Run" : "Download & run") {
                        quick.start(item.id, model: model, installedIDs: installedIDs, completion: refresh)
                    }
                    .controlSize(.small).disabled(quick.isBusy)
                }
            }
        }
    }
}
