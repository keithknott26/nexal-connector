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

/// What a sweep says in numbers, next to the capabilities estimate.
struct SweepSummary: Equatable {
    let median: Double
    let fastest: Double
    let slowest: Double
    let failed: Int
    let calls: Int
    /// Median speed as a share of the estimate; nil without an estimate.
    let percentOfExpected: Double?

    init?(_ sweep: RemoteInferenceSweep, expected: Double?) {
        let speeds = sweep.samples.filter { $0.ok && $0.tokensPerSec > 0 }.map(\.tokensPerSec).sorted()
        guard !speeds.isEmpty else { return nil }
        let mid = speeds.count / 2
        median = speeds.count % 2 == 1 ? speeds[mid] : (speeds[mid - 1] + speeds[mid]) / 2
        fastest = speeds[speeds.count - 1]
        slowest = speeds[0]
        failed = sweep.samples.filter { !$0.ok }.count
        calls = sweep.samples.count
        if let expected, expected > 0 { percentOfExpected = median / expected * 100 } else { percentOfExpected = nil }
    }

    /// One line a person can read: "Median 31.2 tokens/s, 94% of the estimate (26 to 35); 1 of 20 calls failed."
    var line: String {
        var text = String(format: "Median %.1f tokens/s (%.1f to %.1f)", median, slowest, fastest)
        if let percentOfExpected { text += String(format: ", %.0f%% of the estimate", percentOfExpected) }
        if failed > 0 { text += "; \(failed) of \(calls) calls failed" }
        return text + "."
    }
}

/// What the capabilities report predicts for a model's generation speed. Token generation is limited by how fast a
/// Mac reads the model from memory, so the estimate is memory bandwidth over model bytes at a realistic efficiency.
/// A pool passes every token through each Mac in turn, so its times add up, plus one network hop per Mac.
enum ExpectedSpeed {
    /// Peak unified-memory bandwidth in GB/s for Apple silicon, by chip name ("Apple M2 Pro" → 200).
    static let bandwidthGBs: [String: Double] = [
        "m1": 68, "m1 pro": 200, "m1 max": 400, "m1 ultra": 800,
        "m2": 100, "m2 pro": 200, "m2 max": 400, "m2 ultra": 800,
        "m3": 100, "m3 pro": 150, "m3 max": 400, "m3 ultra": 819,
        "m4": 120, "m4 pro": 273, "m4 max": 546,
    ]
    /// Share of the peak a real generation reaches.
    static let efficiency = 0.6
    static let defaultHopMs = 1.0

    static func bandwidth(chip: String?) -> Double? {
        guard var name = chip?.lowercased().trimmingCharacters(in: .whitespaces) else { return nil }
        if name.hasPrefix("apple ") { name.removeFirst("apple ".count) }
        return bandwidthGBs[name]
    }

    /// Tokens per second for one model on the Macs that would hold it; nil when any chip is unknown.
    static func tokensPerSec(model: InferenceReport.Model, machines: [InferenceReport.Machine], links: [InferenceReport.Link]) -> Double? {
        var parts: [(bytes: Double, machine: InferenceReport.Machine)] = []
        if let layout = model.layout, !layout.isEmpty {
            for rank in layout {
                guard let m = machines.first(where: { $0.id == rank.machineId }) else { return nil }
                parts.append((Double(rank.requiredBytes), m))
            }
        } else {
            let host = machines.first(where: { $0.hostId != nil && $0.hostId == model.hostId }) ?? machines.first(where: { $0.isSelf })
            guard let host else { return nil }
            parts.append((Double(model.requiredBytesSingleHost), host))
        }
        var seconds = 0.0
        for part in parts {
            guard let gbs = bandwidth(chip: part.machine.chip), part.bytes > 0 else { return nil }
            seconds += part.bytes / (gbs * 1e9 * efficiency)
        }
        if parts.count > 1 {
            let latencies = links.filter { $0.online }.compactMap(\.latencyMs)
            let hopMs = latencies.isEmpty ? defaultHopMs : latencies.reduce(0, +) / Double(latencies.count)
            seconds += Double(parts.count) * hopMs / 1000
        }
        return seconds > 0 ? 1 / seconds : nil
    }
}

/// Expected against actual speed over many calls: a line per call, a dashed line for the estimate.
struct SpeedSweepView: View {
    let sweep: RemoteInferenceSweep
    let expected: Double?

    private var good: [RemoteInferenceSweep.Sample] { sweep.samples.filter { $0.ok } }
    private var failures: [RemoteInferenceSweep.Sample] { sweep.samples.filter { !$0.ok } }
    private var top: Double {
        let peak = max(good.map(\.tokensPerSec).max() ?? 0, expected ?? 0)
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
                    RuleMark(y: .value("tokens/s", expected))
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
            Text(expected == nil ? "No estimate: this Mac's chip is not in the capabilities table." : "Estimate: memory bandwidth ÷ model size at 60% efficiency.")
                .font(.caption2).foregroundStyle(.secondary)
        }
    }
}
