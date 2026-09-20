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
    func testPausingDoesNotChangeTheTransportIndicator() throws {
        let paused = try status(paused: true)
        let capability = ConnectorStatusCapabilitySource().capability(from: paused)
        XCTAssertEqual(TransportPresentation(capability: capability).mechanism, .unknown)
        XCTAssertEqual(ResourceSharingPresentation(status: paused).state, .pausedByOwner)
        XCTAssertNotEqual(TransportPresentation(capability: capability).indicator.heading,
                          ResourceSharingPresentation(status: paused).indicator.heading)
    }
    // The owner-activity line moved out of the view; the words did not change.
    func testOwnerActivityLineKeepsEveryExistingString() throws {
        func status(known: Bool, synthetic: Bool, ownerActive: Bool, override: Bool) throws -> ConnectorStatus {
            let object: [String: Any] = [
                "paused": false,
                "ownerActivityOverride": override,
                "telemetry": ["known": known, "synthetic": synthetic, "ownerActive": ownerActive]
            ]
            return try ConnectorStatus.decode(try JSONSerialization.data(withJSONObject: object))
        }
        XCTAssertEqual(ResourceSharingPresentation.ownerActivityLine(
            status: try status(known: true, synthetic: true, ownerActive: true, override: false)),
                       "Synthetic development telemetry")
        XCTAssertEqual(ResourceSharingPresentation.ownerActivityLine(
            status: try status(known: false, synthetic: false, ownerActive: false, override: false)),
                       "Telemetry unknown — Go admission fails closed")
        XCTAssertEqual(ResourceSharingPresentation.ownerActivityLine(
            status: try status(known: true, synthetic: false, ownerActive: true, override: true)),
                       "Owner active; private work explicitly permitted")
        XCTAssertEqual(ResourceSharingPresentation.ownerActivityLine(
            status: try status(known: true, synthetic: false, ownerActive: true, override: false)),
                       "Owner active; owner priority applies")
        XCTAssertEqual(ResourceSharingPresentation.ownerActivityLine(
            status: try status(known: true, synthetic: false, ownerActive: false, override: false)),
                       "Owner idle")
    }

    func testOwnerActivityLineIsNilWithoutTelemetry() throws {
        XCTAssertNil(ResourceSharingPresentation.ownerActivityLine(status: nil))
        let noTelemetry = try ConnectorStatus.decode(Data(#"{"paused":false}"#.utf8))
        XCTAssertNil(ResourceSharingPresentation.ownerActivityLine(status: noTelemetry))
    }

    // Synthetic telemetry wins the line, exactly as the previous layout did.
    func testSyntheticNoticeStillExistsSeparately() throws {
        let state = ResourceSharingPresentation(status: try status(synthetic: true))
        XCTAssertEqual(state.syntheticNotice, "Synthetic development telemetry")
    }
}
