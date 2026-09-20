import Foundation
import XCTest
@testable import NexalMac

final class TransportCapabilityTests: XCTestCase {
    private let source = ConnectorStatusCapabilitySource()

    private func status(transport: String?) throws -> ConnectorStatus {
        var object: [String: Any] = ["paused": false]
        if let transport { object["transport"] = transport }
        return try ConnectorStatus.decode(try JSONSerialization.data(withJSONObject: object))
    }

    // Nothing reports RDMA today, so unknown is the default state, not an edge
    // case, and it must be reached without any status at all.
    func testNoStatusMeansNothingIsReportingAndNothingIsClaimed() {
        let capability = source.capability(from: nil)
        XCTAssertTrue(capability.rdma.isEmpty)
        for subsystem in TransportSubsystem.allCases {
            XCTAssertEqual(capability.rdmaState(for: subsystem), .unknown)
        }
        XCTAssertEqual(capability.mechanism, .unknown)
        XCTAssertFalse(capability.sourceReporting)
        XCTAssertNil(capability.reportedText)
    }

    func testBlankTransportIsTreatedAsNoReport() throws {
        for raw in [nil, "", "   "] as [String?] {
            let capability = source.capability(from: try status(transport: raw))
            XCTAssertFalse(capability.sourceReporting)
            XCTAssertTrue(capability.rdma.isEmpty)
        }
    }

    // The two strings the Go agent actually emits today.
    func testGoHeartbeatStringIsControlPlaneOnlyAndLeavesRDMAUnknown() throws {
        let capability = source.capability(
            from: try status(transport: "heartbeat-only; production tunnel dispatch unavailable"))
        XCTAssertEqual(capability.mechanism, .controlPlaneOnly)
        XCTAssertEqual(capability.rdmaState(for: .memoryPager), .unknown)
        XCTAssertEqual(capability.rdmaState(for: .computeCollectives), .unknown)
        XCTAssertTrue(capability.sourceReporting)
    }

    func testGoDevelopmentPullStringIsRelayedAndLeavesRDMAUnknown() throws {
        let raw = "outbound HTTP(S) private pull prototype — NOT protected by incoming PQ tunnel"
        let capability = source.capability(from: try status(transport: raw))
        XCTAssertEqual(capability.mechanism, .coordinatorMediated)
        XCTAssertTrue(capability.rdma.isEmpty)
        XCTAssertEqual(capability.reportedText, raw)
    }

    // Silence is never a denial: this source can never conclude "unavailable".
    func testTheGoSourceNeverReportsRDMAUnavailable() throws {
        for raw in [nil, "heartbeat-only; production tunnel dispatch unavailable",
                    "outbound HTTP(S) private pull prototype", "something unrecognised"] as [String?] {
            let capability = source.capability(from: try status(transport: raw))
            XCTAssertTrue(capability.subsystems(in: .unavailable).isEmpty)
        }
    }

    func testEstablishedRDMAReportIsTheOnlyRouteToAvailable() throws {
        let capability = source.capability(
            from: try status(transport: "native Thunderbolt RDMA link established"))
        XCTAssertEqual(capability.mechanism, .nativeThunderboltRDMA)
        // This source speaks only for the pager path; the compute collectives
        // are owned elsewhere and are never answered for here (§29.8).
        XCTAssertEqual(capability.rdmaState(for: .memoryPager), .available)
        XCTAssertEqual(capability.rdmaState(for: .computeCollectives), .unknown)
    }

    func testPrivateLANReportResolvesToTheFixedProductName() throws {
        let capability = source.capability(
            from: try status(transport: "TCP private-LAN transport to paired peer"))
        XCTAssertEqual(capability.mechanism, .tcpPrivateLAN)
        XCTAssertTrue(capability.rdma.isEmpty)
    }

    // Text that mentions RDMA while saying it is absent is not an RDMA link.
    func testMentioningRDMAWhileAbsentIsNeverResolvedToRDMA() {
        for raw in ["thunderbolt RDMA unavailable; using TCP private-LAN transport",
                    "RDMA unsupported on this host",
                    "rdma disabled, tcp lan fallback"] {
            XCTAssertNotEqual(TransportMechanism.resolve(reportedText: raw), .nativeThunderboltRDMA)
        }
    }

    func testResolutionIsCaseInsensitiveAndOrdered() {
        XCTAssertEqual(TransportMechanism.resolve(reportedText: "NATIVE THUNDERBOLT RDMA ESTABLISHED"),
                       .nativeThunderboltRDMA)
        XCTAssertEqual(TransportMechanism.resolve(reportedText: nil), .unknown)
        XCTAssertEqual(TransportMechanism.resolve(reportedText: "relayed via coordinator"),
                       .coordinatorMediated)
    }

    func testNotReportedIsTheHonestDefault() {
        let capability = TransportCapability.notReported(sourceName: "a future source")
        XCTAssertTrue(capability.rdma.isEmpty)
        XCTAssertEqual(capability.mechanism, .unknown)
        XCTAssertFalse(capability.sourceReporting)
    }
}
