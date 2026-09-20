import Foundation
import XCTest
@testable import NexalMac

final class ConnectorHistoryTests: XCTestCase {
    private let start = Date(timeIntervalSince1970: 1_000)

    private func status(paused: Bool = false, known: Bool = true, synthetic: Bool = false,
                        ownerActive: Bool = false, available: UInt64 = 8 << 20,
                        attempt: String? = nil, policy: Bool = true) throws -> ConnectorStatus {
        var object: [String: Any] = [
            "paused": paused,
            "telemetry": ["known": known, "synthetic": synthetic,
                          "ownerActive": ownerActive, "availableMemoryBytes": available]
        ]
        if policy {
            object["resourcePolicy"] = ["memoryLimitBytes": 268_435_456,
                                        "reserveMemoryBytes": 4_294_967_296,
                                        "idleSeconds": 300]
        }
        if let attempt { object["activeAttempt"] = attempt }
        return try ConnectorStatus.decode(try JSONSerialization.data(withJSONObject: object))
    }

    func testEmptyHistoryHasNoPointsForAnyChart() {
        let history = ConnectorHistory()
        XCTAssertTrue(history.memoryPoints.isEmpty)
        XCTAssertTrue(history.ownerActivityPoints.isEmpty)
        XCTAssertTrue(history.jobActivityPoints.isEmpty)
        XCTAssertTrue(history.throughputPoints.isEmpty)
        XCTAssertFalse(history.hasConnectedSample)
        XCTAssertNil(history.latestAvailableMemoryMiB)
    }

    func testMemorySeriesReportsTheConnectorsApprovedSplitInMiB() throws {
        var history = ConnectorHistory()
        history.record(try status(), at: start)
        XCTAssertEqual(history.memoryPoints.count, 2)
        let approved = history.memoryPoints.first { $0.series == ConnectorHistory.Series.approved }
        let reserved = history.memoryPoints.first { $0.series == ConnectorHistory.Series.reserved }
        XCTAssertEqual(approved?.value, 256)
        XCTAssertEqual(reserved?.value, 4_096)
        XCTAssertEqual(approved?.at, start)
        XCTAssertEqual(history.latestAvailableMemoryMiB, 8)
    }

    func testMissingPolicyDrawsNothingRatherThanZero() throws {
        var history = ConnectorHistory()
        history.record(try status(policy: false), at: start)
        XCTAssertTrue(history.memoryPoints.isEmpty)
        XCTAssertTrue(history.hasConnectedSample)
    }

    func testFailedPollRecordsAGapAndNoMeasuredValues() {
        var history = ConnectorHistory()
        history.record(nil, at: start)
        XCTAssertEqual(history.samples.count, 1)
        XCTAssertFalse(history.samples[0].connected)
        XCTAssertFalse(history.hasConnectedSample)
        XCTAssertTrue(history.memoryPoints.isEmpty)
        XCTAssertTrue(history.ownerActivityPoints.isEmpty)
        XCTAssertTrue(history.jobActivityPoints.isEmpty)
    }

    // Unknown is not idle. A sample with unknown telemetry must not become a
    // zero on the owner-activity chart.
    func testUnknownTelemetryOmitsTheOwnerActivityPoint() throws {
        var history = ConnectorHistory()
        history.record(try status(known: false, ownerActive: true), at: start)
        history.record(try status(known: true, ownerActive: true), at: start.addingTimeInterval(10))
        XCTAssertNil(history.samples[0].ownerActive)
        XCTAssertEqual(history.ownerActivityPoints.count, 1)
        XCTAssertEqual(history.ownerActivityPoints[0].value, 1)
        XCTAssertNil(history.samples[0].availableMemoryMiB)
    }

    func testIdleOwnerIsAMeasuredZero() throws {
        var history = ConnectorHistory()
        history.record(try status(ownerActive: false), at: start)
        XCTAssertEqual(history.ownerActivityPoints.count, 1)
        XCTAssertEqual(history.ownerActivityPoints[0].value, 0)
    }

    func testCompletionIsCountedOnceWhenAnAttemptFingerprintEnds() throws {
        var history = ConnectorHistory()
        history.record(try status(attempt: "attempt-a"), at: start)
        history.record(try status(attempt: "attempt-a"), at: start.addingTimeInterval(10))
        history.record(try status(attempt: nil), at: start.addingTimeInterval(20))
        history.record(try status(attempt: nil), at: start.addingTimeInterval(30))
        XCTAssertEqual(history.samples.map(\.completionsObserved), [0, 0, 1, 0])
        XCTAssertEqual(history.samples.map(\.attemptRunning), [true, true, false, false])
        let completed = history.jobActivityPoints
            .filter { $0.series == ConnectorHistory.Series.completed }
            .map(\.value)
        XCTAssertEqual(completed, [0, 0, 1, 0])
    }

    func testReplacedAttemptCountsTheEndedOneAndKeepsRunningAtOne() throws {
        var history = ConnectorHistory()
        history.record(try status(attempt: "attempt-a"), at: start)
        history.record(try status(attempt: "attempt-b"), at: start.addingTimeInterval(10))
        XCTAssertEqual(history.samples[1].completionsObserved, 1)
        XCTAssertTrue(history.samples[1].attemptRunning)
    }

    func testBlankAttemptStringIsNotAnAttempt() throws {
        var history = ConnectorHistory()
        history.record(try status(attempt: "   "), at: start)
        XCTAssertFalse(history.samples[0].attemptRunning)
        XCTAssertEqual(history.samples[0].completionsObserved, 0)
    }

    func testWindowIsBoundedAndDropsTheOldestSamples() throws {
        var history = ConnectorHistory()
        for index in 0..<(ConnectorHistory.capacity + 25) {
            history.record(try status(), at: start.addingTimeInterval(Double(index) * 10))
        }
        XCTAssertEqual(history.samples.count, ConnectorHistory.capacity)
        XCTAssertEqual(history.samples.first?.at, start.addingTimeInterval(250))
        XCTAssertEqual(history.samples.last?.at,
                       start.addingTimeInterval(Double(ConnectorHistory.capacity + 24) * 10))
        // Identity stays unique after eviction, so chart rows do not collide.
        XCTAssertEqual(Set(history.samples.map(\.id)).count, ConnectorHistory.capacity)
        XCTAssertEqual(Set(history.memoryPoints.map(\.id)).count, history.memoryPoints.count)
        XCTAssertEqual(Set(history.jobActivityPoints.map(\.id)).count, history.jobActivityPoints.count)
    }

    func testSyntheticTelemetryIsRecordedPerSample() throws {
        var history = ConnectorHistory()
        history.record(try status(synthetic: true), at: start)
        XCTAssertTrue(history.samples[0].syntheticTelemetry)
    }

    // Throughput has no source field, so the series must stay empty and say so.
    func testThroughputIsNeverInvented() throws {
        var history = ConnectorHistory()
        for index in 0..<5 {
            history.record(try status(), at: start.addingTimeInterval(Double(index) * 10))
        }
        XCTAssertTrue(history.throughputPoints.isEmpty)
        XCTAssertTrue(ConnectorHistory.throughputUnavailable.contains("No data yet"))
    }

    func testEveryEmptyChartMessageSaysNoDataYet() {
        XCTAssertTrue(ConnectorHistory.throughputUnavailable.hasPrefix("No data yet"))
    }
}
