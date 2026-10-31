import Charts
import Foundation
import SwiftUI

/// What the connector writes to `inference-throughput.json` next to its configuration every two seconds
/// (connector/internal/remoteinference/meter.go): tokens per second returned by this Mac's own model and by
/// requests offloaded to a GPU or cloud tier, in five-second steps over the last ten minutes. No prompt or answer
/// text is in it.
struct InferenceThroughput: Decodable, Equatable {
    struct Point: Decodable, Equatable {
        let t: Int
        let localTps: Double
        let offloadTps: Double
        let localRequests: Int
        let offloadRequests: Int
    }
    struct Totals: Decodable, Equatable {
        let localRequests: Int
        let localTokens: Int
        let offloadRequests: Int
        let offloadTokens: Int
    }
    /// One chat request: its (estimated) prompt size and where it was answered.
    struct Request: Decodable, Equatable {
        let t: Int
        let promptTokens: Int
        let offloaded: Bool
    }
    let updated: Int
    let since: Int
    let bucketSeconds: Int
    let offloadName: String
    let points: [Point]
    let totals: Totals
    /// Recent requests and the "long prompt" limit above which they are offloaded. Absent in files written by an
    /// older connector.
    let requests: [Request]?
    let maxPromptTokens: Int?

    static let fileName = "inference-throughput.json"
    /// The connector rewrites the file every 2 s while there is activity; this much older means it is not running.
    static let staleAfter: TimeInterval = 20

    /// The snapshot beside `config`, or nil when there is none or it cannot be read.
    static func read(besides config: URL) -> InferenceThroughput? {
        let url = config.deletingLastPathComponent().appendingPathComponent(fileName)
        guard let attrs = try? FileManager.default.attributesOfItem(atPath: url.path),
              let size = attrs[.size] as? Int, size > 0, size <= 1 << 20,
              let data = try? Data(contentsOf: url) else { return nil }
        return try? JSONDecoder().decode(InferenceThroughput.self, from: data)
    }

    /// The stats URL for a Mac's model endpoint ("http://100.x.y.z:8001/v1", or just "100.x.y.z:8001"), or nil
    /// unless it is a neXal-network or loopback address.
    static func remoteURL(_ endpoint: String) -> URL? {
        let t = endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return nil }
        let full = t.contains("://") ? t : "http://" + t
        guard RemoteInferenceID.isMeshEndpoint(full), let u = URL(string: full), let host = u.host, let port = u.port else { return nil }
        return URL(string: "http://\(host):\(port)/nexal/throughput")
    }

    /// Asks another Mac's model server for its snapshot. Throws a short, readable reason.
    static func fetch(endpoint: String) async throws -> InferenceThroughput {
        guard let url = remoteURL(endpoint) else { throw FetchError.badAddress }
        var req = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: 4)
        req.httpMethod = "GET"
        let (data, resp) = try await URLSession.shared.data(for: req)
        guard let code = (resp as? HTTPURLResponse)?.statusCode else { throw FetchError.unreachable }
        guard code == 200 else { throw code == 404 ? FetchError.needsUpdate : FetchError.unreachable }
        guard data.count <= 1 << 20, let snap = try? JSONDecoder().decode(InferenceThroughput.self, from: data) else { throw FetchError.unreachable }
        return snap
    }

    enum FetchError: LocalizedError {
        case badAddress, unreachable, needsUpdate
        var errorDescription: String? {
            switch self {
            case .badAddress: return "Use a neXal address like 100.x.y.z:8001."
            case .unreachable: return "Could not reach that Mac's model server."
            case .needsUpdate: return "That Mac answered but does not share throughput yet. Update neXal Connector there."
            }
        }
    }

    var offloadLabel: String { offloadName.isEmpty ? "Offloaded" : offloadName }
    static let localLabel = "This Mac"

    func isStale(now: Date = Date()) -> Bool { now.timeIntervalSince1970 - Double(updated) > Self.staleAfter }

    /// True once anything has been returned since the connector started.
    var hasActivity: Bool { totals.localTokens + totals.offloadTokens + totals.localRequests + totals.offloadRequests > 0 }

    struct Sample: Identifiable, Equatable {
        let time: Date
        let series: String
        let tokensPerSecond: Double
        var id: String { "\(series)-\(time.timeIntervalSince1970)" }
    }

    /// One sample per step and line, for the chart. Pure, so it is unit-tested.
    func samples() -> [Sample] {
        points.flatMap { p -> [Sample] in
            let when = Date(timeIntervalSince1970: Double(p.t))
            return [Sample(time: when, series: Self.localLabel, tokensPerSecond: p.localTps),
                    Sample(time: when, series: offloadLabel, tokensPerSecond: p.offloadTps)]
        }
    }

    /// The average rate over the last `seconds` seconds: the number shown beside each line's name.
    func recentRate(offloaded: Bool, seconds: Int = 15) -> Double {
        let steps = max(1, seconds / max(bucketSeconds, 1))
        let tail = points.suffix(steps)
        guard !tail.isEmpty else { return 0 }
        return tail.map { offloaded ? $0.offloadTps : $0.localTps }.reduce(0, +) / Double(tail.count)
    }

    static func rate(_ tps: Double) -> String {
        tps >= 100 ? String(format: "%.0f tok/s", tps) : String(format: "%.1f tok/s", tps)
    }
}

/// Reads the snapshot every two seconds while the view is on screen.
@MainActor
final class InferenceThroughputModel: ObservableObject {
    @Published private(set) var snapshot: InferenceThroughput?
    /// Why the other Mac's numbers are not shown; nil for this Mac or when they are.
    @Published private(set) var problem: String?
    private var timer: Timer?
    private var fetching = false

    /// `remote` nil: this Mac's own file. Otherwise the model endpoint of the Mac that serves the model.
    func start(config: URL, remote: String? = nil) {
        stop()
        snapshot = nil
        problem = nil
        func tick() {
            if let remote {
                guard !fetching else { return }
                fetching = true
                Task { @MainActor in
                    defer { fetching = false }
                    do { snapshot = try await InferenceThroughput.fetch(endpoint: remote); problem = nil }
                    catch { snapshot = nil; problem = error.localizedDescription }
                }
            } else {
                snapshot = InferenceThroughput.read(besides: config)
            }
        }
        tick()
        timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { _ in Task { @MainActor in tick() } }
    }

    func stop() {
        timer?.invalidate()
        timer = nil
    }
}

/// Under AI inference: a line graph of the tokens per second returned, with one line for this Mac's own model and
/// a separate line for requests offloaded to the GPU (or cloud) tier.
struct InferenceThroughputView: View {
    @EnvironmentObject private var app: AppModel
    @StateObject private var model = InferenceThroughputModel()
    /// "": this Mac. Otherwise the model endpoint of another Mac (remembered between launches).
    @AppStorage("throughputSource") private var source = ""
    @State private var served: [String] = []
    @State private var typed = ""

    private let localColor = Color.blue
    private let offloadColor = Color.orange

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("THROUGHPUT").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
            sourcePicker
            if let s = model.snapshot, !(source.isEmpty && s.isStale()) {
                if s.hasActivity {
                    chart(s)
                    HStack(alignment: .top, spacing: 16) {
                        legend(InferenceThroughput.localLabel, localColor, rate: s.recentRate(offloaded: false),
                               requests: s.totals.localRequests, tokens: s.totals.localTokens)
                        legend(s.offloadLabel, offloadColor, rate: s.recentRate(offloaded: true),
                               requests: s.totals.offloadRequests, tokens: s.totals.offloadTokens)
                    }
                    Text("Tokens returned per second over the last 10 minutes (about one per streamed chunk). Totals since the connector started.")
                        .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    promptSection(s)
                } else {
                    Text("No requests yet. Answers returned by this Mac's model, and by the GPU when you offload, appear here.")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
            } else if !source.isEmpty {
                Text(model.problem ?? "Loading…").font(.caption).foregroundStyle(.secondary)
            } else {
                Text("Start the connector to see throughput. If another Mac serves the model, choose it above.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .onAppear { restart(); loadServed() }
        .onChange(of: source) { _ in restart() }
        .onDisappear { model.stop() }
        .accessibilityIdentifier("inference-throughput")
    }

    private func restart() { model.start(config: app.selectedConfig, remote: source.isEmpty ? nil : source) }

    /// The model endpoints of Macs this Mac has asked to serve a model, for the picker.
    private func loadServed() {
        Task {
            guard let data = try? await app.remoteInference(.remoteInferenceList) else { return }
            let cards = RemoteInferenceCard.cards(RemoteInferenceRequest.decodeLines(data))
            served = cards.filter { $0.state == "ready" }.compactMap { $0.addresses.first { RemoteInferenceID.kind($0) == "neXal network" } }
        }
    }

    @ViewBuilder
    private var sourcePicker: some View {
        HStack(spacing: 8) {
            Picker("Show", selection: $source) {
                Text("This Mac").tag("")
                ForEach(Array(Set(served + (source.isEmpty || served.contains(source) ? [] : [source]))).sorted(), id: \.self) { e in
                    Text(URL(string: e)?.host ?? e).tag(e)
                }
            }
            .pickerStyle(.menu).fixedSize()
            TextField("Mac serving the model, e.g. 100.64.0.5:8001", text: $typed)
                .textFieldStyle(.roundedBorder).font(.caption)
                .onSubmit { if InferenceThroughput.remoteURL(typed) != nil { source = typed.contains("://") ? typed : "http://" + typed; typed = "" } }
        }
        .accessibilityIdentifier("throughput-source")
    }

    /// Each request as a dot at its prompt size (blue: answered here, orange: offloaded) and the offload limit as a
    /// red line: raise the limit in the offload settings and the line moves up, and fewer dots turn orange.
    @ViewBuilder
    private func promptSection(_ s: InferenceThroughput) -> some View {
        let reqs = s.requests ?? []
        let limit = s.maxPromptTokens ?? 0
        if limit > 0 || !reqs.isEmpty {
            let top = Double(max(limit * 5 / 4, (reqs.map(\.promptTokens).max() ?? 0) * 11 / 10, 100))
            VStack(alignment: .leading, spacing: 4) {
                Text("PROMPT SIZE AND THE OFFLOAD LIMIT").font(.caption2.weight(.semibold)).tracking(0.6)
                    .foregroundStyle(.secondary)
                Chart {
                    ForEach(Array(reqs.enumerated()), id: \.offset) { _, r in
                        PointMark(x: .value("Time", Date(timeIntervalSince1970: Double(r.t))),
                                  y: .value("Prompt tokens", r.promptTokens))
                            .foregroundStyle(r.offloaded ? offloadColor : localColor)
                    }
                    if limit > 0 {
                        RuleMark(y: .value("Offload limit", limit))
                            .foregroundStyle(.red)
                            .lineStyle(StrokeStyle(lineWidth: 1.5))
                            .annotation(position: .top, alignment: .trailing) {
                                Text("offloaded above \(limit) tokens").font(.caption2).foregroundStyle(.red)
                            }
                    }
                }
                .chartYScale(domain: 0...top)
                .chartYAxisLabel("prompt tokens", position: .top, alignment: .leading)
                .chartXAxis {
                    AxisMarks(values: .stride(by: .minute, count: 2)) { _ in
                        AxisGridLine()
                        AxisValueLabel(format: .dateTime.hour().minute())
                    }
                }
                .frame(height: 110)
                Text(limit > 0
                     ? "Each dot is a request. Prompts longer than the red line are sent to \(s.offloadLabel)."
                     : "Each dot is a request. No long-prompt limit is set under Offload.")
                    .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private func chart(_ s: InferenceThroughput) -> some View {
        Chart(s.samples()) { sample in
            LineMark(x: .value("Time", sample.time), y: .value("Tokens per second", sample.tokensPerSecond))
                .foregroundStyle(by: .value("Where", sample.series))
                .interpolationMethod(.monotone)
        }
        .chartForegroundStyleScale(domain: [InferenceThroughput.localLabel, s.offloadLabel], range: [localColor, offloadColor])
        .chartLegend(.hidden)
        .chartYAxisLabel("tok/s", position: .top, alignment: .leading)
        .chartXAxis {
            AxisMarks(values: .stride(by: .minute, count: 2)) { _ in
                AxisGridLine()
                AxisValueLabel(format: .dateTime.hour().minute())
            }
        }
        .frame(height: 110)
    }

    private func legend(_ name: String, _ color: Color, rate: Double, requests: Int, tokens: Int) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            HStack(spacing: 4) {
                Circle().fill(color).frame(width: 7, height: 7)
                Text(name).font(.caption.weight(.semibold))
            }
            Text(InferenceThroughput.rate(rate)).font(.caption).monospacedDigit()
            Text("\(requests) requests, \(tokens) tokens").font(.caption2).foregroundStyle(.secondary)
        }
    }
}
