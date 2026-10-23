import SwiftUI

/// One-click AI inference: pick a model, and the app sets up the MLX runtime, pins and downloads the model
/// (every file hash-checked by the connector) and runs a local OpenAI-compatible chat server on this Mac.
/// All work happens in `nexal inference setup | pin --latest | install | serve`; this file only sequences
/// them and shows progress. The server binds 127.0.0.1 and also answers on this Mac's neXal (mesh) address and
/// LAN address when they are known, so the owner's other Macs and devices on the LAN can use it too.
struct QuickStartModel: Identifiable, Equatable {
    let id: String
    let title: String
    let detail: String
}

@MainActor
final class InferenceQuickStart: ObservableObject {
    /// Inside 8001-8099, the range the neXal network opens between the owner's Macs.
    static let port = 8001
    static let models: [QuickStartModel] = [
        .init(id: "qwen3-4b-q4", title: "Qwen3 4B", detail: "2.3 GB download · quick, runs on any Mac"),
        .init(id: "qwen3-8b-q4", title: "Qwen3 8B", detail: "4.6 GB download · needs about 8 GB free memory"),
        .init(id: "qwen3-14b-q4", title: "Qwen3 14B", detail: "8.3 GB download · needs about 16 GB free memory"),
        .init(id: "phi-4-mini-q4", title: "Phi-4 mini", detail: "2.1 GB download · small and fast"),
    ]

    enum Phase: Equatable {
        case idle
        case working(model: String, step: String, fraction: Double?)
        case serving(model: String, urls: [ServeURL])
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

    /// One address the running model answers on.
    struct ServeURL: Equatable, Hashable { let label: String; let url: String }

    /// The addresses to answer on besides loopback: this Mac's neXal address and LAN address (IPv4 only).
    static func extraAddresses(mesh: String?, lan: String?) -> [(label: String, ip: String)] {
        [("neXal network", mesh), ("LAN", lan)].compactMap { label, ip in
            guard let ip, isIPv4(ip), ip != "127.0.0.1" else { return nil }
            return (label, ip)
        }
    }

    /// neXal first, then the LAN, then loopback (this Mac only).
    static func urls(port: Int, extra: [(label: String, ip: String)]) -> [ServeURL] {
        extra.map { ServeURL(label: $0.label, url: "http://\($0.ip):\(port)/v1") }
            + [ServeURL(label: "This Mac", url: "http://127.0.0.1:\(port)/v1")]
    }

    /// The addresses the helper really answers on, from its `{"event":"listening","urls":[...]}` line.
    nonisolated static func listening(_ line: Data) -> [ServeURL]? {
        struct Line: Decodable { let event: String; let urls: [String]? }
        guard let l = try? JSONDecoder().decode(Line.self, from: line), l.event == "listening", let urls = l.urls, !urls.isEmpty else { return nil }
        return urls.map { ServeURL(label: label(of: $0), url: $0) }
    }

    nonisolated static func label(of url: String) -> String {
        guard let host = URL(string: url)?.host else { return "Address" }
        let p = host.split(separator: ".").compactMap { Int($0) }
        if host == "127.0.0.1" { return "This Mac" }
        if p.count == 4 && p[0] == 100 && (64...127).contains(p[1]) { return "neXal network" }
        return "LAN"
    }

    static func isIPv4(_ s: String) -> Bool {
        let parts = s.split(separator: ".", omittingEmptySubsequences: false)
        return parts.count == 4 && parts.allSatisfy { p in p.count <= 3 && Int(p).map { (0...255).contains($0) } == true }
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
                    // The app ships a reviewed commit and file hashes for each model, so there is nothing to pin:
                    // the download is checked against them as it arrives.
                    var progress = InstallProgress(modelId: id)
                    var failure: (code: String, message: String)?
                    phase = .working(model: id, step: "Downloading the model…", fraction: nil)
                    let result = try await stream(.inferenceInstallLocal(modelId: id), model: model) { [self] line in
                        guard let event = try? InferenceInstallEvent.decode(line: line) else { return }
                        if case let .error(code, message, _) = event { failure = (code, message) }
                        progress.apply(event)
                        phase = .working(model: id, step: "Downloading \(id): \(progress.percentLabel) · \(progress.sizeLabel)", fraction: progress.fraction)
                    }
                    if stopping { phase = .idle; return }
                    if result.status != 0 && failure?.code != "already_installed" {
                        if failure?.code == "not_installable" { throw QuickStartError(Self.notReviewed) }
                        throw QuickStartError(failure?.message ?? result.reason ?? "The download stopped without saying why.")
                    }
                }

                let status = model.status
                let lan = status?.presence?.hosts?.first { $0.hostId == status?.hostId }?.info.lanAddress
                let extra = Self.extraAddresses(mesh: status?.mesh?.selfTunnelAddress, lan: lan)
                phase = .serving(model: id, urls: Self.urls(port: Self.port, extra: extra))
                // The macOS firewall drops connections from other devices to a helper it does not allow, and an
                // update re-signs the helper, which voids an earlier allowance. Ask once to allow it again.
                if await Task.detached(operation: { NetworkService.firewallBlocksHelpers }).value {
                    phase = .working(model: id, step: "Allowing your other computers through the macOS firewall…", fraction: nil)
                    // Cancelling the password prompt is not fatal: the model still answers on this Mac.
                    _ = try? await Task.detached(operation: { try NetworkService.allowHelpersInFirewall() }).value
                    if stopping { phase = .idle; return }
                }
                // The helper finds every neXal and LAN address itself ("auto") and says which ones answer.
                let result = try await stream(.inferenceServe(modelId: id, port: Self.port, alsoListen: [InferenceQuickStart.autoListen], json: true),
                                              model: model) { [self] line in
                    if let urls = Self.listening(line) { phase = .serving(model: id, urls: urls) }
                }
                if stopping { phase = .idle; return }
                phase = .failed(result.reason ?? "The model server stopped. Check that this Mac has enough free memory for \(id).")
            } catch {
                phase = stopping ? .idle : .failed(error.localizedDescription)
            }
        }
    }

    static let autoListen = "auto"

    static let notReviewed = "This model has not been reviewed for this version of neXal yet. Update neXal, or pick another model."

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
    /// Models with a reviewed commit in this build; nil until known (then every model is shown).
    @State private var reviewed: Set<String>? = nil

    private var shown: [QuickStartModel] {
        guard let reviewed else { return InferenceQuickStart.models }
        return InferenceQuickStart.models.filter { reviewed.contains($0.id) || installedIDs.contains($0.id) }
    }

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
            case let .serving(id, urls):
                VStack(alignment: .leading, spacing: 6) {
                    Label("\(id) is running", systemImage: "bolt.horizontal.circle.fill").font(.callout.weight(.semibold)).foregroundStyle(.green)
                    ForEach(urls, id: \.self) { u in
                        CopyableBlock(title: "\(u.label) (OpenAI-compatible)", lines: [u.url])
                    }
                    Text("No sign-in: anyone who can reach an address above can use the model.").font(.caption2).foregroundStyle(.tertiary)
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
            if shown.isEmpty {
                Text("No model has been reviewed for this version of neXal yet. Update neXal to get the next set.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            ForEach(shown) { item in
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
        .task { reviewed = await model.reviewedModelIDs() }
    }
}
