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

    // D7 regression. Every case above contains a denial word ("unavailable",
    // "unsupported", "disabled", "fallback"), which is why the old condition
    // survived review: `text.contains("thunderbolt")` resolved ANY text naming the
    // cable to .nativeThunderboltRDMA, and none of these phrases carries a denial
    // word. resolveCapability() then sets rdma[.memoryPager] = .available, so this
    // was a false CAPABILITY claim, not merely a wrong label. Thunderbolt 4
    // carries IP and cannot do RDMA at all.
    func testNamingTheCableWithoutRDMAIsNeverResolvedToRDMA() {
        for raw in ["IP over Thunderbolt",
                    "ip over thunderbolt bridge established",
                    "Thunderbolt bridge up",
                    "connected over Thunderbolt 4",
                    "thunderbolt 5 cable detected",
                    "peer reachable via thunderbolt"] {
            XCTAssertNotEqual(TransportMechanism.resolve(reportedText: raw),
                              .nativeThunderboltRDMA,
                              "naming the cable must not claim RDMA: \(raw)")
        }
    }

    // IP over a Thunderbolt bridge is a private-LAN TCP transport.
    func testIPOverThunderboltResolvesToPrivateLAN() {
        for raw in ["IP over Thunderbolt", "thunderbolt bridge established",
                    "Thunderbolt IP link up"] {
            XCTAssertEqual(TransportMechanism.resolve(reportedText: raw), .tcpPrivateLAN)
        }
    }

    // The fix must not weaken genuine RDMA detection.
    func testExplicitRDMAStillResolves() {
        for raw in ["native Thunderbolt RDMA link established",
                    "RDMA established", "rdma link up over thunderbolt 5"] {
            XCTAssertEqual(TransportMechanism.resolve(reportedText: raw),
                           .nativeThunderboltRDMA)
        }
    }

    // A denial word must still win even when RDMA is named alongside the cable.
    func testDenialStillBeatsExplicitRDMA() {
        XCTAssertNotEqual(
            TransportMechanism.resolve(reportedText: "ip over thunderbolt; rdma unavailable"),
            .nativeThunderboltRDMA)
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
