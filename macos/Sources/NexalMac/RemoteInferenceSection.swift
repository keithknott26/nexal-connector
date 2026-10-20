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
    /// Set when several Macs serve the model together; rank 0 answers.
    let poolId: String?
    let poolRank: Int?
    let poolSize: Int?

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

/// What the list shows: one request to one Mac, or a pool (several requests with the same pool id) as a single card.
struct RemoteInferenceCard: Identifiable, Equatable {
    struct Member: Equatable { let hostId: String; let state: String; let step: String?; let detail: String? }
    /// The request to stop or test: the single request, or rank 0 of a pool (stopping it stops the whole pool).
    let id: String
    let modelId: String
    let state: String
    let step: String?
    let detail: String?
    let endpoint: String?
    let progress: Double?
    let members: [Member]
    var isPool: Bool { members.count > 1 }

    static func cards(_ requests: [RemoteInferenceRequest]) -> [RemoteInferenceCard] {
        var order: [String] = []
        var groups: [String: [RemoteInferenceRequest]] = [:]
        for r in requests where r.state != "cancelled" {
            let key = r.poolId ?? r.id
            if groups[key] == nil { order.append(key) }
            groups[key, default: []].append(r)
        }
        return order.compactMap { key -> RemoteInferenceCard? in
            guard let group = groups[key]?.sorted(by: { ($0.poolRank ?? 0) < ($1.poolRank ?? 0) }), let lead = group.first else { return nil }
            let members = group.map { Member(hostId: $0.targetHostId, state: $0.state, step: $0.step, detail: $0.detail) }
            if group.count == 1 {
                return RemoteInferenceCard(id: lead.id, modelId: lead.modelId, state: lead.state, step: lead.step, detail: lead.detail, endpoint: lead.endpoint, progress: lead.progress, members: members)
            }
            // Show the Mac that really failed, not the others that stopped because of it.
            let failed = group.first { $0.state == "failed" && $0.step != "pool" } ?? group.first { $0.state == "failed" }
            // The slowest Mac sets the pace: show its step and detail.
            let slowest = group.filter { !$0.isDone }.min { (RemoteInferenceStages.index(of: $0.step) ?? -1) < (RemoteInferenceStages.index(of: $1.step) ?? -1) }
            let state = failed != nil ? "failed" : lead.state == "ready" ? "ready" : group.allSatisfy { $0.state == "pending" } ? "pending" : "running"
            // Nothing reported yet means no figure (an indeterminate bar), not 0%.
            let reported = group.contains { $0.progress != nil || $0.state == "ready" }
            let progress: Double? = reported ? group.map { $0.progress ?? ($0.state == "ready" ? 1 : 0) }.reduce(0, +) / Double(group.count) : nil
            // Once the Mac that answers is serving (alone, or with the others), its message is the news: "serving on this
            // Mac alone", "the pool lost a member". Before that, the slowest Mac sets the pace.
            let shown = failed ?? (lead.state == "ready" || lead.step == "serve" ? lead : slowest)
            return RemoteInferenceCard(id: lead.id, modelId: lead.modelId, state: state, step: shown?.step, detail: shown?.detail,
                                       endpoint: lead.endpoint, progress: state == "running" ? progress : nil, members: members)
        }
    }
}

@MainActor
final class RemoteInferenceModel: ObservableObject {
    @Published var machines: [InferenceReport.Machine] = []
    /// Every Mac in the report, this one included, for naming the target of a request.
    @Published var allMachines: [InferenceReport.Machine] = []
    @Published var requests: [RemoteInferenceRequest] = []
    @Published var problem: String?
    /// Models that install with one click on this build; nil until known.
    @Published var reviewed: Set<String>?
    @Published var busy = false
    @Published var testing: Set<String> = []
    @Published var results: [String: RemoteInferenceTestResult] = [:]
    @Published var sweeping: Set<String> = []
    @Published var sweeps: [String: RemoteInferenceSweep] = [:]
    /// The capabilities estimate of tokens per second for each request that was swept; absent when unknown.
    @Published var expected: [String: Double] = [:]
    private var lastReport: InferenceReport?
    private var polling: Task<Void, Never>?

    func loadMachines(_ app: AppModel) {
        Task {
            reviewed = await app.reviewedModelIDs()
            do {
                let report = try InferenceReport.decode(try await app.inferencePreflight())
                lastReport = report
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

    /// Online Macs that can join a pool, the one with the most memory first (it answers).
    var poolMembers: [InferenceReport.Machine] {
        allMachines.filter { $0.online && $0.hostId?.isEmpty == false }.sorted { $0.totalMemoryBytes > $1.totalMemoryBytes }
    }

    func servePool(modelId: String, app: AppModel) {
        let ids = poolMembers.compactMap(\.hostId)
        guard ids.count >= 2 else { problem = "A pool needs at least two Macs that are online."; return }
        busy = true; problem = nil
        Task {
            defer { busy = false }
            do { _ = try await app.remoteInference(.remoteInferencePool(modelId: modelId, hostIds: Array(ids.prefix(8)))); await refresh(app) }
            catch { problem = "Could not start the pool: \(error.localizedDescription)" }
        }
    }

    func stop(_ id: String, app: AppModel) {
        Task {
            do { _ = try await app.remoteInference(.remoteInferenceStop(requestId: id)); await refresh(app) }
            catch { problem = "Could not stop it: \(error.localizedDescription)" }
        }
    }

    /// Twenty generations in a row against the model's address, drawn against what the capabilities report expects.
    func sweep(id: String, modelId: String, endpoint: String?, app: AppModel, calls: Int = 20) {
        guard let endpoint else { return }
        sweeping.insert(id); problem = nil
        expected[id] = nil
        if let report = lastReport, let model = report.models.first(where: { $0.id == modelId }) {
            expected[id] = ExpectedSpeed.tokensPerSec(model: model, machines: report.machines, links: report.links)
        }
        Task {
            defer { sweeping.remove(id) }
            do {
                let data = try await app.remoteInference(.remoteInferenceSweep(endpoint: endpoint, calls: calls))
                sweeps[id] = try JSONDecoder().decode(RemoteInferenceSweep.self, from: data)
            } catch { problem = "The speed sweep failed: \(error.localizedDescription)" }
        }
    }

    func test(id: String, endpoint: String?, app: AppModel) {
        guard let endpoint else { return }
        testing.insert(id); problem = nil
        Task {
            defer { testing.remove(id) }
            do {
                let data = try await app.remoteInference(.remoteInferenceTest(endpoint: endpoint))
                results[id] = try JSONDecoder().decode(RemoteInferenceTestResult.self, from: data)
            } catch { problem = "The test failed: \(error.localizedDescription)" }
        }
    }
}

/// The "on other Macs" half of the AI inference card: pick a Mac and a model, watch it install and serve.
/// It sits inside `InferenceSection`, under the one-click "this Mac" quick start.
struct RemoteInferenceContent: View {
    @EnvironmentObject private var app: AppModel
    @StateObject private var remote = RemoteInferenceModel()
    @State private var hostId = ""
    @State private var modelId = InferenceQuickStart.models.first?.id ?? "qwen3-4b-q4"

    private func name(_ id: String) -> String {
        guard let m = remote.allMachines.first(where: { $0.hostId == id }) else { return "another Mac" }
        return m.isSelf ? "this Mac" : m.name
    }

    @ViewBuilder private var poolControls: some View {
        let members = remote.poolMembers
        if members.count >= 2 {
            let running = RemoteInferenceCard.cards(remote.requests).contains { $0.isPool && $0.modelId == modelId && $0.state != "failed" }
            let memory = ByteCountFormatter.string(fromByteCount: Int64(members.reduce(UInt64(0)) { $0 + $1.totalMemoryBytes }), countStyle: .memory)
            VStack(alignment: .leading, spacing: 4) {
                Text("POOL YOUR MACS").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
                Text("Spread one model over \(members.count) online Macs (\(members.map(\.name).joined(separator: ", ")); \(memory) memory together). Worth it for a model too big for one Mac; each word is slower than on a single Mac.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                Button("Pool \(members.count) Macs") { remote.servePool(modelId: modelId, app: app) }
                    .buttonStyle(.bordered).controlSize(.small)
                    .disabled(remote.busy || running)
                    .help(running ? "This model is already running on a pool." : "Run the selected model on all online Macs together.")
            }
        }
    }

    private var models: [QuickStartModel] {
        guard let reviewed = remote.reviewed else { return InferenceQuickStart.models }
        return InferenceQuickStart.models.filter { reviewed.contains($0.id) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("RUN A MODEL ON ANOTHER MAC").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
            Text("Run a model on one of your other Macs and use it from here over the neXal network. That Mac downloads the model and keeps it private to your network.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if remote.machines.isEmpty {
                Text("No other Macs found on your network yet.").font(.caption).foregroundStyle(.secondary)
            } else {
                HStack {
                    Picker("Mac", selection: $hostId) {
                        ForEach(remote.machines) { Text($0.name + ($0.online ? "" : " (offline)")).tag($0.hostId ?? "") }
                    }.labelsHidden().frame(maxWidth: 180)
                    Picker("Model", selection: $modelId) {
                        ForEach(models) { Text($0.title).tag($0.id) }
                    }.labelsHidden().frame(maxWidth: 160)
                    Button("Run there") { remote.serve(modelId: modelId, hostId: hostId, app: app) }
                        .buttonStyle(.borderedProminent).controlSize(.small)
                        .disabled(remote.busy || hostId.isEmpty)
                }
            }
            poolControls
            if let p = remote.problem {
                Label(p, systemImage: "exclamationmark.triangle.fill").font(.caption).foregroundStyle(.orange).textSelection(.enabled)
            }
            ForEach(RemoteInferenceCard.cards(remote.requests)) { c in row(c) }
        }
        .task {
            remote.loadMachines(app)
            remote.startPolling(app)
        }
        .onChange(of: remote.reviewed) { _, _ in
            if !models.contains(where: { $0.id == modelId }), let first = models.first { modelId = first.id }
        }
        .onChange(of: remote.machines) { _, list in
            if hostId.isEmpty || !list.contains(where: { $0.hostId == hostId }) {
                hostId = (list.first(where: { $0.online }) ?? list.first)?.hostId ?? ""
            }
        }
        .onDisappear { remote.stopPolling() }
    }

    @ViewBuilder private func row(_ c: RemoteInferenceCard) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(title(c)).font(.caption.weight(.semibold)).fixedSize(horizontal: false, vertical: true)
                Spacer()
                switch c.state {
                case "ready": Label("Serving", systemImage: "checkmark.circle.fill").font(.caption).foregroundStyle(.green)
                case "failed": Label("Failed", systemImage: "xmark.octagon.fill").font(.caption).foregroundStyle(.red)
                default: ProgressView().controlSize(.small)
                }
            }
            if c.state == "running" || c.state == "pending" {
                if let f = c.progress, c.state == "running" {
                    HStack(spacing: 6) {
                        ProgressView(value: min(max(f, 0), 1))
                        Text("\(Int(min(max(f, 0), 1) * 100))%").font(.caption2.monospacedDigit()).foregroundStyle(.secondary)
                    }
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel("Progress")
                    .accessibilityValue("\(Int(min(max(f, 0), 1) * 100)) percent")
                } else {
                    ProgressView().progressViewStyle(.linear)
                }
            }
            if c.state != "ready" && c.state != "pending" { stepsView(c) }
            if let d = c.detail, c.state != "ready" || c.isPool {
                Text(d).font(.caption2).foregroundStyle(c.state == "failed" ? .red : RemoteInferenceStages.isWarning(d) ? .orange : .secondary).textSelection(.enabled)
            }
            if c.state == "pending" { Text(c.isPool ? "Waiting for the Macs in the pool to pick this up…" : "Waiting for that Mac to pick this up…").font(.caption2).foregroundStyle(.secondary) }
            if c.isPool { memberLines(c) }
            if c.state == "ready", let e = c.endpoint {
                HStack {
                    Text(e).font(.system(.caption2, design: .monospaced)).textSelection(.enabled)
                    Button("Copy") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(e, forType: .string) }
                        .buttonStyle(.link).font(.caption2)
                }
                HStack {
                    Button(remote.testing.contains(c.id) ? "Testing…" : "Test speed & capability") { remote.test(id: c.id, endpoint: c.endpoint, app: app) }
                        .controlSize(.small).disabled(remote.testing.contains(c.id))
                    if remote.testing.contains(c.id) { ProgressView().controlSize(.small) }
                    Button(remote.sweeping.contains(c.id) ? "Sweeping…" : "Speed sweep (20 calls)") {
                        remote.sweep(id: c.id, modelId: c.modelId, endpoint: c.endpoint, app: app)
                    }
                    .controlSize(.small).disabled(remote.sweeping.contains(c.id))
                    if remote.sweeping.contains(c.id) { ProgressView().controlSize(.small) }
                }
                if let t = remote.results[c.id] { resultView(t) }
                if let sw = remote.sweeps[c.id] { SpeedSweepView(sweep: sw, expected: remote.expected[c.id]) }
            }
            Button(c.state == "failed" ? "Dismiss" : (c.isPool ? "Stop pool" : "Stop"), role: .destructive) { remote.stop(c.id, app: app) }
                .controlSize(.small)
        }
        .padding(8)
        .background(RoundedRectangle(cornerRadius: 8).fill(Color.secondary.opacity(0.08)))
    }

    private func title(_ c: RemoteInferenceCard) -> String {
        c.isPool ? "\(c.modelId) across \(c.members.map { name($0.hostId) }.joined(separator: ", "))" : "\(c.modelId) on \(name(c.members[0].hostId))"
    }

    /// One line per Mac in a pool while it is coming up, so a slow or failed Mac is easy to spot.
    @ViewBuilder private func memberLines(_ c: RemoteInferenceCard) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            ForEach(Array(c.members.enumerated()), id: \.offset) { _, m in
                HStack(spacing: 4) {
                    Image(systemName: m.state == "failed" ? "xmark.circle.fill" : m.state == "ready" ? "checkmark.circle.fill" : "circle.dotted")
                        .foregroundStyle(m.state == "failed" ? .red : m.state == "ready" ? .green : .secondary)
                    Text("\(name(m.hostId)): \(RemoteInferenceStages.label(m.state, m.step))").foregroundStyle(.secondary)
                }
            }
        }.font(.caption2)
    }

    /// The stages of a request, in order. `RemoteInferenceStages.index(of:)` maps the step the other Mac reports.
    @ViewBuilder private func stepsView(_ c: RemoteInferenceCard) -> some View {
        let current = RemoteInferenceStages.index(of: c.step)
        let failed = c.state == "failed"
        HStack(spacing: 10) {
            ForEach(Array((c.isPool ? RemoteInferenceStages.pool : RemoteInferenceStages.all).enumerated()), id: \.offset) { i, title in
                HStack(spacing: 3) {
                    if let cur = current, i < cur {
                        Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
                    } else if current == i {
                        Image(systemName: failed ? "xmark.circle.fill" : "circle.dotted").foregroundStyle(failed ? .red : .accentColor)
                    } else {
                        Image(systemName: "circle").foregroundStyle(.tertiary)
                    }
                    Text(title).foregroundStyle(current == i ? .primary : .secondary)
                }
            }
        }
        .font(.caption2)
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

/// The stages the other Mac goes through, matching the steps its connector reports.
enum RemoteInferenceStages {
    static let all = ["Runtime", "Check", "Download", "Verify", "Start"]
    /// A pool waits for the other Macs where a single Mac verifies.
    static let pool = ["Runtime", "Check", "Download", "Join pool", "Start"]
    private static let keys = ["setup", "pin", "install", "verify", "serve"]
    /// A short phrase for what one Mac in a pool is doing.
    static func label(_ state: String, _ step: String?) -> String {
        if state == "ready" { return "ready" }
        if state == "failed" { return "failed" }
        if state == "pending" { return "waiting" }
        switch step {
        case "setup": return "preparing the runtime"
        case "pin": return "checking the model"
        case "install": return "downloading"
        case "pool": return "waiting for the pool"
        case "serve": return "loading the model"
        default: return "working"
        }
    }
    /// Pool notices worth the owner's attention: a Mac dropped out, or the pool could not form.
    static func isWarning(_ detail: String) -> Bool { detail.contains("lost a member") || detail.contains("Could not form") || detail.contains("Left the pool") }
    static func index(of step: String?) -> Int? { step == "pool" ? 3 : step.flatMap { keys.firstIndex(of: $0) } }
}
