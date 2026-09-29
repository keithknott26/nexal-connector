import Foundation

/// Bounded, in-memory rolling window behind every chart in the panel
/// (HARDENING-PLAN §26.4).
///
/// Nothing here is persisted. The window exists for the lifetime of this app
/// process and is discarded on quit, so the connector writes no new telemetry
/// file for the sake of a chart and `CONTRACT.md` is unchanged. One status poll
/// appends exactly one sample; this type owns no timer, and neither does any
/// chart, so graphs cannot multiply into extra polling.
///
/// Pure value logic, no SwiftUI, so the derived series are unit-testable.
struct ConnectorHistory: Equatable {
    /// 30 minutes at the panel's ten-second poll. Oldest samples are dropped.
    static let capacity = 180

    /// Throughput has no source in the Go connector's status schema: it reports
    /// no per-transport byte counters. The chart therefore stays empty rather
    /// than drawing a zero, which would read as a measurement (§26.4).
    static let throughputUnavailable = """
        No data yet. The Go connector's status reports no per-transport byte \
        counters, so throughput is not measured on this Mac. Nothing is drawn \
        instead of a zero line, which would read as a measured value.
        """

    /// What the job chart can and cannot claim, stated next to it.
    static let jobActivityLimits = """
        Observed at the status poll: an attempt that starts and finishes between \
        two polls is not counted. The connector reports one active attempt and a \
        last outcome, so accepted and running are not separable from it.
        """

    static let memoryCaption = """
        The connector's approved policy, as it reports it. A flat line is a stable \
        policy, not an idle measurement.
        """

    static let ownerActivityCaption = """
        Jobs run only when your activity allows it. When your activity is unknown, \
        no point is shown, because unknown does not mean idle.
        """

    /// Series names, defined once so the charts and the tests cannot drift.
    enum Series {
        static let approved = "Approved for sharing"
        static let reserved = "Kept for owner"
        static let ownerActive = "Owner active"
        static let attempt = "Active attempt"
        static let completed = "Completed (observed)"
    }

    struct Sample: Identifiable, Equatable {
        let id: Int
        let at: Date
        /// False when the poll produced no status at all. Such a sample records
        /// the gap and contributes no measured value to any series.
        let connected: Bool
        let approvedMemoryMiB: Double?
        let reservedMemoryMiB: Double?
        let availableMemoryMiB: Double?
        /// nil when the connector reported unknown telemetry. Never coerced to
        /// false, because unknown is not idle.
        let ownerActive: Bool?
        let syntheticTelemetry: Bool
        let attemptRunning: Bool
        /// Completions this app actually observed between the previous poll and
        /// this one. Go owns the real outcome; this counts transitions only.
        let completionsObserved: Int
        /// Each secure-network peer as the connector reported it at this poll.
        var peers: [PeerSample] = []
    }

    /// One peer's latency and cumulative traffic counters at one poll.
    struct PeerSample: Equatable {
        let id: String
        let name: String
        let connected: Bool
        let latencyMs: Double?
        let sentBytes: UInt64
        let receivedBytes: UInt64
    }

    static let networkLatencyCaption = """
        Average round-trip time to the other Macs on your neXal network. \
        Gaps mean no other Mac was connected.
        """
    static let peerLatencyCaption = """
        Round-trip time to each Mac, one line per Mac.
        """
    static let trafficCaption = """
        Data sent to (out) and received from (in) each Mac, averaged over each interval.
        """

    /// One point in one chart series.
    struct SeriesPoint: Identifiable, Equatable {
        let id: Int
        let at: Date
        let series: String
        let value: Double
    }

    private(set) var samples: [Sample] = []
    private var nextID = 0
    private var previousAttempt = ""

    /// Appends one sample. Called once per poll from `AppModel`, never from a view.
    mutating func record(_ status: ConnectorStatus?, at date: Date = Date()) {
        let attempt = status?.activeAttempt?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        // An attempt fingerprint that disappears or is replaced is one attempt
        // this app saw end. It is not proof of success; the connector's own
        // outcome remains authoritative.
        let completed = (!previousAttempt.isEmpty && attempt != previousAttempt) ? 1 : 0
        previousAttempt = attempt
        let telemetry = status?.telemetry
        let known = telemetry?.known == true
        samples.append(Sample(
            id: nextID,
            at: date,
            connected: status != nil,
            approvedMemoryMiB: Self.mib(status?.resourcePolicy?.memoryLimitBytes),
            reservedMemoryMiB: Self.mib(status?.resourcePolicy?.reserveMemoryBytes),
            availableMemoryMiB: known ? Self.mib(telemetry?.availableMemoryBytes) : nil,
            ownerActive: known ? telemetry?.ownerActive : nil,
            syntheticTelemetry: telemetry?.synthetic == true,
            attemptRunning: !attempt.isEmpty,
            completionsObserved: completed,
            peers: (status?.mesh?.peers ?? []).map {
                PeerSample(id: $0.id, name: $0.name.isEmpty ? $0.id : $0.name,
                           connected: $0.lifecycle == "connected", latencyMs: $0.latencyMs,
                           sentBytes: $0.traffic.sentBytes, receivedBytes: $0.traffic.receivedBytes)
            }))
        nextID += 1
        if samples.count > Self.capacity {
            samples.removeFirst(samples.count - Self.capacity)
        }
    }

    private static func mib(_ bytes: UInt64?) -> Double? {
        guard let bytes else { return nil }
        return Double(bytes) / (1_024 * 1_024)
    }

    /// Memory contributed versus kept for the owner. A sample with no reported
    /// policy contributes nothing, so a missing policy shows as "no data yet".
    var memoryPoints: [SeriesPoint] {
        var points: [SeriesPoint] = []
        for sample in samples {
            if let approved = sample.approvedMemoryMiB {
                points.append(SeriesPoint(id: sample.id * 4, at: sample.at,
                                          series: Series.approved, value: approved))
            }
            if let reserved = sample.reservedMemoryMiB {
                points.append(SeriesPoint(id: sample.id * 4 + 1, at: sample.at,
                                          series: Series.reserved, value: reserved))
            }
        }
        return points
    }

    /// Active versus idle, since it gates admission. Unknown telemetry is a gap.
    var ownerActivityPoints: [SeriesPoint] {
        samples.compactMap { sample -> SeriesPoint? in
            guard let active = sample.ownerActive else { return nil }
            return SeriesPoint(id: sample.id * 4 + 2, at: sample.at,
                               series: Series.ownerActive, value: active ? 1 : 0)
        }
    }

    /// Attempts seen running, and completions seen ending, per poll.
    var jobActivityPoints: [SeriesPoint] {
        var points: [SeriesPoint] = []
        for sample in samples where sample.connected {
            points.append(SeriesPoint(id: sample.id * 4 + 3, at: sample.at,
                                      series: Series.attempt,
                                      value: sample.attemptRunning ? 1 : 0))
            points.append(SeriesPoint(id: sample.id * 4 + 4, at: sample.at,
                                      series: Series.completed,
                                      value: Double(sample.completionsObserved)))
        }
        return points
    }

    /// Mean latency across connected peers, one point per poll that had any.
    var networkLatencyPoints: [SeriesPoint] {
        samples.compactMap { sample -> SeriesPoint? in
            let values = sample.peers.filter(\.connected).compactMap(\.latencyMs).filter { $0 > 0 }
            guard !values.isEmpty else { return nil }
            return SeriesPoint(id: sample.id, at: sample.at, series: "neXal network",
                               value: values.reduce(0, +) / Double(values.count))
        }
    }

    /// Latency per peer; the series name is the peer's name.
    var peerLatencyPoints: [SeriesPoint] {
        var points: [SeriesPoint] = []
        for sample in samples {
            for peer in sample.peers where peer.connected {
                guard let latency = peer.latencyMs, latency > 0 else { continue }
                points.append(SeriesPoint(id: points.count, at: sample.at, series: peer.name, value: latency))
            }
        }
        return points
    }

    /// Transfer rate in KB/s per peer and direction, from the change in the
    /// cumulative counters between consecutive polls. A counter that went
    /// backwards (runtime restart) contributes no point rather than a negative.
    var trafficPoints: [SeriesPoint] {
        var points: [SeriesPoint] = []
        for (previous, sample) in zip(samples, samples.dropFirst()) {
            let seconds = sample.at.timeIntervalSince(previous.at)
            guard seconds > 0 else { continue }
            for peer in sample.peers {
                guard let before = previous.peers.first(where: { $0.id == peer.id }) else { continue }
                if peer.receivedBytes >= before.receivedBytes {
                    points.append(SeriesPoint(id: points.count, at: sample.at, series: "\(peer.name) · in",
                        value: Double(peer.receivedBytes - before.receivedBytes) / 1_000 / seconds))
                }
                if peer.sentBytes >= before.sentBytes {
                    points.append(SeriesPoint(id: points.count, at: sample.at, series: "\(peer.name) · out",
                        value: Double(peer.sentBytes - before.sentBytes) / 1_000 / seconds))
                }
            }
        }
        return points
    }

    /// Always empty. See `throughputUnavailable`: the field does not exist, so
    /// no line is drawn and no number is invented.
    var throughputPoints: [SeriesPoint] { [] }

    /// Most recent measured free memory, for a caption next to the memory chart.
    var latestAvailableMemoryMiB: Double? {
        samples.last(where: { $0.availableMemoryMiB != nil })?.availableMemoryMiB
    }

    /// Whether any poll has ever produced a status. Used to choose between
    /// "no data yet" and a drawn chart.
    var hasConnectedSample: Bool { samples.contains { $0.connected } }
}
