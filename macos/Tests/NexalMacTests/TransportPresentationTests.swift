import Foundation
import XCTest
@testable import NexalMac

final class TransportPresentationTests: XCTestCase {
    private func status(transport: String?) throws -> ConnectorStatus {
        var object: [String: Any] = ["paused": false]
        if let transport { object["transport"] = transport }
        return try ConnectorStatus.decode(try JSONSerialization.data(withJSONObject: object))
    }

    // The two strings the Go agent actually emits today.
    func testGoHeartbeatStringIsControlPlaneOnly() throws {
        let state = TransportPresentation(
            status: try status(transport: "heartbeat-only; production tunnel dispatch unavailable"))
        XCTAssertEqual(state.mechanism, .controlPlaneOnly)
        XCTAssertEqual(state.label, "No data transport established")
        XCTAssertEqual(state.indicator.tone, .grey)
    }

    func testGoDevelopmentPullStringIsRelayed() throws {
        let raw = "outbound HTTP(S) private pull prototype — NOT protected by incoming PQ tunnel"
        let state = TransportPresentation(status: try status(transport: raw))
        XCTAssertEqual(state.mechanism, .coordinatorMediated)
        XCTAssertEqual(state.label, "Relayed through the coordinator")
        // The connector's own warning must survive the switch to an indicator row.
        XCTAssertTrue(state.indicator.reason.contains(raw))
    }

    func testMissingOrBlankTransportIsUnknownAndNotUnavailable() throws {
        for raw in [nil, "", "   "] as [String?] {
            let state = TransportPresentation(status: try status(transport: raw))
            XCTAssertEqual(state.mechanism, .unknown)
            XCTAssertEqual(state.label, "Transport unknown")
            XCTAssertNil(state.connectorReported)
        }
        XCTAssertEqual(TransportPresentation(status: nil).mechanism, .unknown)
    }

    func testEstablishedRDMAAndPrivateLANUseTheFixedProductNames() throws {
        XCTAssertEqual(TransportPresentation(
            status: try status(transport: "native Thunderbolt RDMA link established")).label,
                       "Native Thunderbolt RDMA")
        XCTAssertEqual(TransportPresentation(
            status: try status(transport: "TCP private-LAN transport to paired peer")).label,
                       "TCP private-LAN transport")
    }

    // A string that merely mentions RDMA is not an RDMA link. The UI must never
    // read "Thunderbolt 5 detected" out of an absence.
    func testMentioningRDMAWhileAbsentIsNeverReportedAsRDMA() throws {
        for raw in ["thunderbolt RDMA unavailable; using TCP private-LAN transport",
                    "RDMA unsupported on this host",
                    "rdma disabled, tcp lan fallback"] {
            let state = TransportPresentation(status: try status(transport: raw))
            XCTAssertNotEqual(state.mechanism, .nativeThunderboltRDMA)
        }
    }

    func testEveryStateCarriesALabelASymbolAndAReason() throws {
        let samples: [String?] = [nil, "heartbeat-only; x", "native Thunderbolt RDMA established",
                                  "TCP private-LAN transport", "relayed via coordinator", "mystery"]
        for raw in samples {
            let indicator = TransportPresentation(status: try status(transport: raw)).indicator
            XCTAssertFalse(indicator.label.isEmpty)
            XCTAssertFalse(indicator.systemImage.isEmpty)
            XCTAssertFalse(indicator.reason.isEmpty)
            XCTAssertNotNil(indicator.detail)
            XCTAssertEqual(indicator.heading, "Active transport")
        }
    }

    func testNoStateClaimsThunderboltFiveDetection() throws {
        for raw in [nil, "heartbeat-only; production tunnel dispatch unavailable",
                    "outbound HTTP(S) private pull prototype"] as [String?] {
            let indicator = TransportPresentation(status: try status(transport: raw)).indicator
            let text = "\(indicator.label) \(indicator.reason) \(indicator.detail ?? "")"
            XCTAssertFalse(text.contains("Thunderbolt 5 detected"))
        }
    }
}
