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

    /// What a Mac serving a model attaches to its coordinator heartbeat (connector/internal/remoteinference/meter.go,
    /// `Stats`), as `nexal inference remote-stats` returns it: per-minute counts, recent prompt sizes and the limit.
    struct Wire: Decodable, Equatable {
        struct Minute: Decodable, Equatable { let t: Int; let lt: Int; let ot: Int; let lr: Int; let or: Int }
        struct Prompt: Decodable, Equatable { let t: Int; let p: Int; let o: Bool }
        let at: Int
        let since: Int
        let offloadName: String
        let localRequests: Int
        let localTokens: Int
        let offloadRequests: Int
        let offloadTokens: Int
        let maxPromptTokens: Int
        let minutes: [Minute]
        let prompts: [Prompt]
    }

    /// One Mac's report, as the coordinator stored it.
    struct RemoteHost: Decodable, Equatable, Identifiable {
        let hostId: String
        let name: String
        let reportedAt: String
        let stats: Wire
        var id: String { hostId }

        /// When the Mac reported, nil if the timestamp cannot be read.
        var reportedDate: Date? {
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            return f.date(from: reportedAt) ?? ISO8601DateFormatter().date(from: reportedAt)
        }
    }

    /// Decodes `nexal inference remote-stats --json`; an unreadable answer is an empty list.
    static func remoteHosts(_ data: Data) -> [RemoteHost] {
        struct Doc: Decodable { let hosts: [RemoteHost] }
        return (try? JSONDecoder().decode(Doc.self, from: data).hosts) ?? []
    }

    /// The same snapshot the local file holds, built from a report: one point per minute, tokens per second as the
    /// minute's tokens over sixty.
    init(_ w: Wire) {
        updated = w.at
        since = w.since
        bucketSeconds = 60
        offloadName = w.offloadName
        points = w.minutes.map { Point(t: $0.t, localTps: Double($0.lt) / 60, offloadTps: Double($0.ot) / 60,
                                       localRequests: $0.lr, offloadRequests: $0.or) }
        totals = Totals(localRequests: w.localRequests, localTokens: w.localTokens,
                        offloadRequests: w.offloadRequests, offloadTokens: w.offloadTokens)
        requests = w.prompts.map { Request(t: $0.t, promptTokens: $0.p, offloaded: $0.o) }
        maxPromptTokens = w.maxPromptTokens
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
    private var timer: Timer?

    func start(config: URL) {
        stop()
        snapshot = InferenceThroughput.read(besides: config)
        timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.snapshot = InferenceThroughput.read(besides: config) }
        }
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
    /// "": this Mac. Otherwise the host id of another Mac whose report to show (remembered between launches).
    @AppStorage("throughputSource") private var source = ""
    /// What the Macs reported through the coordinator, read only while this view is on screen.
    @State private var reports: [InferenceThroughput.RemoteHost] = []
    @State private var remoteProblem: String?
    @State private var remoteLoaded = false

    private let localColor = Color.blue
    private let offloadColor = Color.orange

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("THROUGHPUT").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
            sourcePicker
            if let s = shown {
                if s.hasActivity {
                    chart(s)
                    HStack(alignment: .top, spacing: 16) {
                        legend(InferenceThroughput.localLabel, localColor, rate: s.recentRate(offloaded: false),
                               requests: s.totals.localRequests, tokens: s.totals.localTokens)
                        legend(s.offloadLabel, offloadColor, rate: s.recentRate(offloaded: true),
                               requests: s.totals.offloadRequests, tokens: s.totals.offloadTokens)
                    }
                    Text(source.isEmpty
                         ? "Tokens returned per second over the last 10 minutes (about one per streamed chunk). Totals since the connector started."
                         : remoteCaption)
                        .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    promptSection(s)
                } else {
                    Text("No requests yet. Answers returned by this Mac's model, and by the GPU when you offload, appear here.")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
            } else if !source.isEmpty {
                Text(remoteProblem ?? (remoteLoaded ? "That Mac has not reported any AI throughput lately. It reports with its heartbeat once it has answered a request, from a build that includes this."
                                                    : "Loading…"))
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            } else {
                Text("Start the connector to see throughput. If another Mac serves the model, choose it above.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .onAppear { model.start(config: app.selectedConfig) }
        .onDisappear { model.stop() }
        // One read of the coordinator when the view opens, then every 30 s only while another Mac is chosen. The
        // reports themselves arrive in that Mac's ordinary heartbeats, so this adds nothing to what the Macs send.
        .task(id: source) {
            while !Task.isCancelled {
                await loadReports()
                if source.isEmpty { break }
                try? await Task.sleep(nanoseconds: 30_000_000_000)
            }
        }
        .accessibilityIdentifier("inference-throughput")
    }

    /// The snapshot to draw: this Mac's own file (ignored when the connector stopped), or the chosen Mac's last report.
    private var shown: InferenceThroughput? {
        if source.isEmpty { return model.snapshot.flatMap { $0.isStale() ? nil : $0 } }
        return reports.first { $0.hostId == source }.map { InferenceThroughput($0.stats) }
    }

    private var remoteCaption: String {
        guard let r = reports.first(where: { $0.hostId == source }) else { return "" }
        let when = r.reportedDate.map { RelativeDateTimeFormatter().localizedString(for: $0, relativeTo: Date()) } ?? "recently"
        return "Reported by \(r.name) \(when), one point per minute. It travels with that Mac's regular heartbeat, so it updates as often as the heartbeat does (every few minutes while the Mac is idle). Totals since its connector started."
    }

    private func loadReports() async {
        do {
            let data = try await app.remoteInference(.remoteInferenceStats)
            reports = InferenceThroughput.remoteHosts(data)
            remoteProblem = nil
        } catch {
            reports = []
            remoteProblem = "Could not read the report from the coordinator: \(error.localizedDescription)"
        }
        remoteLoaded = true
    }

    @ViewBuilder
    private var sourcePicker: some View {
        Picker("Show", selection: $source) {
            Text("This Mac").tag("")
            // Every Mac that has reported, plus the saved choice if it has not reported lately.
            ForEach(reports) { r in Text(r.name).tag(r.hostId) }
            if !source.isEmpty, !reports.contains(where: { $0.hostId == source }) { Text("Saved Mac").tag(source) }
        }
        .pickerStyle(.menu).fixedSize()
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
