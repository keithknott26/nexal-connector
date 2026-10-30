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
    let updated: Int
    let since: Int
    let bucketSeconds: Int
    let offloadName: String
    let points: [Point]
    let totals: Totals

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

    private let localColor = Color.blue
    private let offloadColor = Color.orange

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("THROUGHPUT").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
            if let s = model.snapshot, !s.isStale() {
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
                } else {
                    Text("No requests yet. Answers returned by this Mac's model, and by the GPU when you offload, appear here.")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
            } else {
                Text("Start the connector to see throughput.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .onAppear { model.start(config: app.selectedConfig) }
        .onDisappear { model.stop() }
        .accessibilityIdentifier("inference-throughput")
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
