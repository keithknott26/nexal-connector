import SwiftUI

enum RemoteInferenceID {
    static func isValid(_ s: String) -> Bool {
        s.first?.isASCII == true && (s.first!.isLetter || s.first!.isNumber) && s.count <= 80 && s.allSatisfy { $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "_" || $0 == "-") }
    }
    /// http://100.x.y.z:8001-8099/v1 (a neXal address) or loopback.
    static func isMeshEndpoint(_ s: String) -> Bool {
        guard let u = URL(string: s), u.scheme == "http", u.user == nil, u.query == nil, let host = u.host, u.port != nil else { return false }
        let p = host.split(separator: ".").compactMap { Int($0) }
        guard p.count == 4, p.allSatisfy({ (0...255).contains($0) }) else { return false }
        return host == "127.0.0.1" || (p[0] == 100 && (64...127).contains(p[1]))
    }
}

/// One request to another Mac, as `nexal inference remote-list --json` prints it.
struct RemoteInferenceRequest: Decodable, Identifiable, Equatable {
    let id: String
    let targetHostId: String
    let modelId: String
    let state: String
    let step: String?
    let detail: String?
    let endpoint: String?
    /// 0...1 while the model downloads on the other Mac.
    let progress: Double?

    var isDone: Bool { state == "ready" || state == "failed" || state == "cancelled" }
    static func decodeLines(_ data: Data) -> [RemoteInferenceRequest] {
        data.split(separator: UInt8(ascii: "\n")).compactMap { try? JSONDecoder().decode(RemoteInferenceRequest.self, from: Data($0)) }
    }
}

struct RemoteInferenceTestResult: Decodable, Equatable {
    let endpoint: String
    let model: String
    let reachableMs: Double
    let firstTokenMs: Double
    let totalMs: Double
    let tokens: Int
    let tokensPerSec: Double
    let reply: String
    let capabilityOk: Bool
}

@MainActor
final class RemoteInferenceModel: ObservableObject {
    @Published var machines: [InferenceReport.Machine] = []
    /// Every Mac in the report, this one included, for naming the target of a request.
    @Published var allMachines: [InferenceReport.Machine] = []
    @Published var requests: [RemoteInferenceRequest] = []
    @Published var problem: String?
    @Published var busy = false
    @Published var testing: Set<String> = []
    @Published var results: [String: RemoteInferenceTestResult] = [:]
    private var polling: Task<Void, Never>?

    func loadMachines(_ app: AppModel) {
        Task {
            do {
                let report = try InferenceReport.decode(try await app.inferencePreflight())
                allMachines = report.machines
                machines = report.machines.filter { !$0.isSelf && $0.hostId?.isEmpty == false }
            } catch { problem = "Could not list your other Macs: \(error.localizedDescription)" }
        }
    }

    func refresh(_ app: AppModel) async {
        if let data = try? await app.remoteInference(.remoteInferenceList) { requests = RemoteInferenceRequest.decodeLines(data) }
    }

    /// Keeps the list fresh while any request is still in progress.
    func startPolling(_ app: AppModel) {
        polling?.cancel()
        polling = Task { [weak self] in
            while !Task.isCancelled {
                await self?.refresh(app)
                let active = self?.requests.contains { !$0.isDone } ?? false
                try? await Task.sleep(nanoseconds: active ? 3_000_000_000 : 15_000_000_000)
            }
        }
    }
    func stopPolling() { polling?.cancel() }

    func serve(modelId: String, hostId: String, app: AppModel) {
        busy = true; problem = nil
        Task {
            defer { busy = false }
            do { _ = try await app.remoteInference(.remoteInferenceServe(modelId: modelId, hostId: hostId)); await refresh(app) }
            catch { problem = "Could not send the request: \(error.localizedDescription)" }
        }
    }

    func stop(_ r: RemoteInferenceRequest, app: AppModel) {
        Task {
            do { _ = try await app.remoteInference(.remoteInferenceStop(requestId: r.id)); await refresh(app) }
            catch { problem = "Could not stop it: \(error.localizedDescription)" }
        }
    }

    func test(_ r: RemoteInferenceRequest, app: AppModel) {
        guard let endpoint = r.endpoint else { return }
        testing.insert(r.id); problem = nil
        Task {
            defer { testing.remove(r.id) }
            do {
                let data = try await app.remoteInference(.remoteInferenceTest(endpoint: endpoint))
                results[r.id] = try JSONDecoder().decode(RemoteInferenceTestResult.self, from: data)
            } catch { problem = "The test failed: \(error.localizedDescription)" }
        }
    }
}

struct RemoteInferenceSection: View {
    @Binding var expanded: Bool
    @EnvironmentObject private var app: AppModel
    @StateObject private var remote = RemoteInferenceModel()
    @State private var hostId = ""
    @State private var modelId = InferenceQuickStart.models.first?.id ?? "qwen3-4b-q4"

    private func name(_ id: String) -> String {
        guard let m = remote.allMachines.first(where: { $0.hostId == id }) else { return "another Mac" }
        return m.isSelf ? "this Mac" : m.name
    }

    var body: some View {
        DisclosureGroup(isExpanded: $expanded) {
            VStack(alignment: .leading, spacing: 10) {
                Text("Run an AI model on another one of your Macs and use it from here over the neXal network. The other Mac downloads the model and keeps it private to your network.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                if remote.machines.isEmpty {
                    Text("No other Macs found on your network yet.").font(.caption).foregroundStyle(.secondary)
                } else {
                    HStack {
                        Picker("Mac", selection: $hostId) {
                            ForEach(remote.machines) { Text($0.name + ($0.online ? "" : " (offline)")).tag($0.hostId ?? "") }
                        }.labelsHidden().frame(maxWidth: 180)
                        Picker("Model", selection: $modelId) {
                            ForEach(InferenceQuickStart.models) { Text($0.title).tag($0.id) }
                        }.labelsHidden().frame(maxWidth: 160)
                        Button("Run there") { remote.serve(modelId: modelId, hostId: hostId, app: app) }
                            .buttonStyle(.borderedProminent).controlSize(.small)
                            .disabled(remote.busy || hostId.isEmpty)
                    }
                }
                if let p = remote.problem {
                    Label(p, systemImage: "exclamationmark.triangle.fill").font(.caption).foregroundStyle(.orange).textSelection(.enabled)
                }
                ForEach(remote.requests.filter { $0.state != "cancelled" }) { r in row(r) }
            }
            .padding(.top, 6)
            .task {
                remote.loadMachines(app)
                remote.startPolling(app)
            }
            .onChange(of: remote.machines) { _, list in
                if hostId.isEmpty || !list.contains(where: { $0.hostId == hostId }) {
                    hostId = (list.first(where: { $0.online }) ?? list.first)?.hostId ?? ""
                }
            }
            .onDisappear { remote.stopPolling() }
        } label: {
            Text("AI on other Macs").font(.subheadline.weight(.semibold))
        }
    }

    @ViewBuilder private func row(_ r: RemoteInferenceRequest) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text("\(r.modelId) on \(name(r.targetHostId))").font(.caption.weight(.semibold))
                Spacer()
                switch r.state {
                case "ready": Label("Serving", systemImage: "checkmark.circle.fill").font(.caption).foregroundStyle(.green)
                case "failed": Label("Failed", systemImage: "xmark.octagon.fill").font(.caption).foregroundStyle(.red)
                default: ProgressView().controlSize(.small)
                }
            }
            if r.state == "running", r.step == "install" {
                if let f = r.progress { ProgressView(value: min(max(f, 0), 1)) } else { ProgressView().progressViewStyle(.linear) }
            }
            if let d = r.detail, r.state != "ready" { Text(d).font(.caption2).foregroundStyle(r.state == "failed" ? .red : .secondary).textSelection(.enabled) }
            if r.state == "pending" { Text("Waiting for that Mac to pick this up…").font(.caption2).foregroundStyle(.secondary) }
            if r.state == "ready", let e = r.endpoint {
                HStack {
                    Text(e).font(.system(.caption2, design: .monospaced)).textSelection(.enabled)
                    Button("Copy") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(e, forType: .string) }
                        .buttonStyle(.link).font(.caption2)
                }
                HStack {
                    Button(remote.testing.contains(r.id) ? "Testing…" : "Test speed & capability") { remote.test(r, app: app) }
                        .controlSize(.small).disabled(remote.testing.contains(r.id))
                    if remote.testing.contains(r.id) { ProgressView().controlSize(.small) }
                }
                if let t = remote.results[r.id] { resultView(t) }
            }
            Button(r.state == "failed" ? "Dismiss" : "Stop", role: .destructive) { remote.stop(r, app: app) }
                .controlSize(.small)
        }
        .padding(8)
        .background(RoundedRectangle(cornerRadius: 8).fill(Color.secondary.opacity(0.08)))
    }

    private func resultView(_ t: RemoteInferenceTestResult) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(String(format: "Reachable in %.0f ms · first word in %.0f ms · %.1f words/s", t.reachableMs, t.firstTokenMs, t.tokensPerSec))
            Label(t.capabilityOk ? "Answered 17 + 25 correctly" : "Did not answer 17 + 25 correctly (\(t.reply))",
                  systemImage: t.capabilityOk ? "checkmark.seal.fill" : "exclamationmark.triangle.fill")
                .foregroundStyle(t.capabilityOk ? .green : .orange)
        }.font(.caption2)
    }
}
