import Charts
import Foundation
import SwiftUI

/// `nexal inference remote-test <endpoint> --calls N --json`: the same generation measured many times in a row.
struct RemoteInferenceSweep: Decodable, Equatable {
    struct Sample: Decodable, Equatable, Identifiable {
        let call: Int
        let firstTokenMs: Double
        let tokensPerSec: Double
        let tokens: Int
        let totalMs: Double
        let ok: Bool
        let error: String?
        var id: Int { call }
    }

    let endpoint: String
    let model: String
    let samples: [Sample]
}

/// What the capabilities report predicts for a model's generation speed, as a range: the lowest and highest
/// speeds a healthy Mac of that kind reaches.
struct ExpectedEstimate: Equatable {
    let low: Double
    let high: Double
    /// The chips the estimate was made for, so a wrong guess is visible.
    let chips: String
}

/// What a sweep says in numbers, next to the estimate.
struct SweepSummary: Equatable {
    enum Verdict: Equatable { case below, inLine, above, unknown }
    let median: Double
    let fastest: Double
    let slowest: Double
    let failed: Int
    let calls: Int
    let verdict: Verdict

    init?(_ sweep: RemoteInferenceSweep, expected: ExpectedEstimate?) {
        let speeds = sweep.samples.filter { $0.ok && $0.tokensPerSec > 0 }.map(\.tokensPerSec).sorted()
        guard !speeds.isEmpty else { return nil }
        let mid = speeds.count / 2
        median = speeds.count % 2 == 1 ? speeds[mid] : (speeds[mid - 1] + speeds[mid]) / 2
        fastest = speeds[speeds.count - 1]
        slowest = speeds[0]
        failed = sweep.samples.filter { !$0.ok }.count
        calls = sweep.samples.count
        if let expected {
            // Some slack on both sides: the estimate is a model, not a measurement.
            if median < expected.low * 0.85 { verdict = .below }
            else if median > expected.high * 1.15 { verdict = .above }
            else { verdict = .inLine }
        } else { verdict = .unknown }
    }

    /// One line a person can read: "Median 31.2 tokens/s (26.0 to 35.0), in line with the estimate; 1 of 20 calls failed."
    var line: String {
        var text = String(format: "Median %.1f tokens/s (%.1f to %.1f)", median, slowest, fastest)
        switch verdict {
        case .below: text += ", below the estimate"
        case .inLine: text += ", in line with the estimate"
        case .above: text += ", above the estimate (the estimate is probably too low for this Mac)"
        case .unknown: break
        }
        if failed > 0 { text += "; \(failed) of \(calls) calls failed" }
        return text + "."
    }
}

/// Token generation is limited by how fast a Mac reads the model's weights from memory, so the speed is memory
/// bandwidth over weight bytes. Bandwidth depends on the chip (and on a few binned variants of Pro and Max
/// chips), and software gets a share of it, so the estimate is a range. A pool passes every token through each
/// Mac in turn, so its times add up, plus one network hop per Mac. A chip this table does not know gets no
/// estimate rather than a wrong one.
enum ExpectedSpeed {
    /// Unified-memory bandwidth in GB/s (lowest and highest variant of the chip), by chip name without "Apple ".
    static let bandwidthGBs: [String: (low: Double, high: Double)] = [
        "m1": (68, 68), "m1 pro": (200, 200), "m1 max": (400, 400), "m1 ultra": (800, 800),
        "m2": (100, 100), "m2 pro": (200, 200), "m2 max": (400, 400), "m2 ultra": (800, 800),
        "m3": (100, 100), "m3 pro": (150, 150), "m3 max": (300, 400), "m3 ultra": (819, 819),
        "m4": (120, 120), "m4 pro": (273, 273), "m4 max": (410, 546),
        "m5": (153, 153),
    ]
    /// The share of that bandwidth generation reaches: a slow run and a good run.
    static let efficiency = (low: 0.6, high: 0.85)
    static let defaultHopMs = 1.0

    static func bandwidth(chip: String?) -> (low: Double, high: Double)? {
        guard var name = chip?.lowercased().trimmingCharacters(in: .whitespaces) else { return nil }
        if name.hasPrefix("apple ") { name.removeFirst("apple ".count) }
        return bandwidthGBs[name]
    }

    /// The range for `weights` bytes shared out over Macs with these chips (`shares` add up to 1).
    static func estimate(weights: Double, shares: [Double], chips: [String?], hopMs: Double) -> ExpectedEstimate? {
        guard weights > 0, !shares.isEmpty, shares.count == chips.count else { return nil }
        var slow = 0.0, fast = 0.0
        for (share, chip) in zip(shares, chips) {
            guard let bw = bandwidth(chip: chip) else { return nil }
            slow += weights * share / (bw.low * 1e9 * efficiency.low)
            fast += weights * share / (bw.high * 1e9 * efficiency.high)
        }
        if shares.count > 1 {
            slow += Double(shares.count) * hopMs / 1000
            fast += Double(shares.count) * hopMs / 1000
        }
        guard slow > 0, fast > 0 else { return nil }
        return ExpectedEstimate(low: 1 / slow, high: 1 / fast, chips: chips.map { $0 ?? "unknown" }.joined(separator: " + "))
    }

    /// The Macs that hold the model right now, leader first. A host that is not in the list is this Mac.
    static func serving(hostIds: [String], machines: [InferenceReport.Machine]) -> [InferenceReport.Machine] {
        var out: [InferenceReport.Machine] = []
        var unmatched = 0
        for id in hostIds {
            if let m = machines.first(where: { $0.hostId == id }) { out.append(m) } else { unmatched += 1 }
        }
        if unmatched > 0, let me = machines.first(where: { $0.isSelf }), !out.contains(where: { $0.isSelf }) { out.append(me) }
        return out
    }

    /// The estimate for a model on the Macs serving it. Weight bytes (the download size) are what generation
    /// reads; the report's required bytes also hold cache and headroom, so they are only a fallback.
    static func estimate(model: InferenceReport.Model, serving: [InferenceReport.Machine], links: [InferenceReport.Link]) -> ExpectedEstimate? {
        guard !serving.isEmpty else { return nil }
        let weights = model.downloadBytes.map(Double.init) ?? Double(model.requiredBytesSingleHost) * 0.85
        var shares = Array(repeating: 1.0 / Double(serving.count), count: serving.count)
        if serving.count > 1, let layout = model.layout {
            let ranked = serving.map { m in layout.first(where: { $0.machineId == m.id }).map { Double($0.requiredBytes) } }
            if ranked.allSatisfy({ $0 != nil }) {
                let total = ranked.compactMap { $0 }.reduce(0, +)
                if total > 0 { shares = ranked.compactMap { $0 }.map { $0 / total } }
            }
        }
        let latencies = links.filter { $0.online }.compactMap(\.latencyMs)
        let hopMs = latencies.isEmpty ? defaultHopMs : latencies.reduce(0, +) / Double(latencies.count)
        return estimate(weights: weights, shares: shares, chips: serving.map(\.chip), hopMs: hopMs)
    }
}

/// Expected against actual speed over many calls: a line per call, a dashed line for the estimate.
struct SpeedSweepView: View {
    let sweep: RemoteInferenceSweep
    let expected: ExpectedEstimate?

    private var good: [RemoteInferenceSweep.Sample] { sweep.samples.filter { $0.ok } }
    private var failures: [RemoteInferenceSweep.Sample] { sweep.samples.filter { !$0.ok } }
    private var top: Double {
        let peak = max(good.map(\.tokensPerSec).max() ?? 0, expected?.high ?? 0)
        return peak <= 0 ? 1 : peak * 1.15
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Chart {
                ForEach(good) { s in
                    LineMark(x: .value("Call", s.call), y: .value("tokens/s", s.tokensPerSec), series: .value("Series", "Actual"))
                        .foregroundStyle(by: .value("Series", "Actual"))
                        .interpolationMethod(.monotone)
                    PointMark(x: .value("Call", s.call), y: .value("tokens/s", s.tokensPerSec))
                        .foregroundStyle(by: .value("Series", "Actual"))
                        .symbolSize(18)
                }
                if let expected {
                    RectangleMark(xStart: .value("Call", 1), xEnd: .value("Call", max(sweep.samples.count, 2)),
                                  yStart: .value("tokens/s", expected.low), yEnd: .value("tokens/s", expected.high))
                        .foregroundStyle(Color.orange.opacity(0.22))
                    RuleMark(y: .value("tokens/s", (expected.low + expected.high) / 2))
                        .foregroundStyle(by: .value("Series", "Expected"))
                        .lineStyle(StrokeStyle(lineWidth: 1.5, dash: [5, 4]))
                }
                ForEach(failures) { s in
                    RuleMark(x: .value("Call", s.call))
                        .foregroundStyle(by: .value("Series", "Failed"))
                        .lineStyle(StrokeStyle(lineWidth: 1))
                }
            }
            .chartForegroundStyleScale(["Actual": Color.accentColor, "Expected": Color.orange, "Failed": Color.red])
            .chartYScale(domain: 0...top)
            .chartYAxisLabel("tokens/s")
            .chartXAxisLabel("call")
            .frame(height: 140)
            if let summary = SweepSummary(sweep, expected: expected) {
                Text(summary.line).font(.caption2)
            }
            Text(expected.map { String(format: "Estimate %.0f to %.0f tokens/s for %@: memory bandwidth ÷ model weights at 60 to 85%% efficiency.", $0.low, $0.high, $0.chips) } ?? "No estimate: the chip is not in the capabilities table.")
                .font(.caption2).foregroundStyle(.secondary)
        }
    }
}
