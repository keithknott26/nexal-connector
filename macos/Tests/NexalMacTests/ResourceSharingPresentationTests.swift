import Foundation
import XCTest
@testable import NexalMac

final class ResourceSharingPresentationTests: XCTestCase {
    private func status(paused: Bool = false, known: Bool = true, synthetic: Bool = false,
                        blocker: String? = nil) throws -> ConnectorStatus {
        var object: [String: Any] = [
            "paused": paused,
            "telemetry": ["known": known, "synthetic": synthetic,
                          "ownerActive": false, "availableMemoryBytes": 1_024]
        ]
        if let blocker { object["executionBlocker"] = blocker }
        return try ConnectorStatus.decode(try JSONSerialization.data(withJSONObject: object))
    }

    func testUnpausedKnownTelemetryWithoutABlockerIsSharing() throws {
        let state = ResourceSharingPresentation(status: try status())
        XCTAssertEqual(state.state, .sharing)
        XCTAssertEqual(state.indicator.tone, .colour)
    }

    func testBlockerKeepsSharingColouredAndShowsTheConnectorsReason() throws {
        let state = ResourceSharingPresentation(
            status: try status(blocker: "insufficient approved memory headroom"))
        XCTAssertEqual(state.state, .waiting)
        XCTAssertEqual(state.indicator.tone, .colour)
        XCTAssertEqual(state.indicator.reason, "Waiting: insufficient approved memory headroom")
    }

    func testPauseTakesPrecedenceAndIsGrey() throws {
        let state = ResourceSharingPresentation(status: try status(paused: true, blocker: "x"))
        XCTAssertEqual(state.state, .pausedByOwner)
        XCTAssertEqual(state.indicator.tone, .grey)
        XCTAssertEqual(state.label, "Paused by you")
    }

    func testUnknownTelemetryKeepsTheFailClosedString() throws {
        for value in [try status(known: false), try ConnectorStatus.decode(Data(#"{"paused":false}"#.utf8))] {
            let state = ResourceSharingPresentation(status: value)
            XCTAssertEqual(state.state, .telemetryUnknown)
            XCTAssertEqual(state.indicator.reason, "Telemetry unknown — Go admission fails closed")
            XCTAssertEqual(state.indicator.tone, .grey)
        }
    }

    func testMissingStatusNeverClaimsSharing() {
        let state = ResourceSharingPresentation(status: nil)
        XCTAssertEqual(state.state, .notConnected)
        XCTAssertEqual(state.indicator.tone, .grey)
        XCTAssertNil(state.syntheticNotice)
    }

    func testSyntheticTelemetryIsAlwaysDisclosed() throws {
        XCTAssertEqual(ResourceSharingPresentation(status: try status(synthetic: true)).syntheticNotice,
                       "Synthetic development telemetry")
        XCTAssertNil(ResourceSharingPresentation(status: try status()).syntheticNotice)
    }

    func testOptInCaveatIsUnchanged() {
        XCTAssertEqual(ResourceSharingPresentation.optInCaveat,
                       "Opt-in permits the Go connector to apply its resource and idle policies. It does not promise that a workload is available or enabled.")
    }

    // The two indicators answer different questions and must not be merged: a
    // paused Mac still reports its transport state, and vice versa (§26.3).
    func testPausingDoesNotChangeTheTransportOrRDMAIndicators() throws {
        let paused = try status(paused: true)
        XCTAssertEqual(TransportPresentation(status: paused).mechanism, .unknown)
        XCTAssertEqual(ResourceSharingPresentation(status: paused).state, .pausedByOwner)
        let rdma = RDMAAvailability(osMajor: 14, osMinor: 0,
                                    isAppleSiliconBuild: true, establishedLink: false)
        XCTAssertEqual(rdma.state, .unavailable)
        XCTAssertNotEqual(rdma.indicator.heading, ResourceSharingPresentation(status: paused).indicator.heading)
    }
}
