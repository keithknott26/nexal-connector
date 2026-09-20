import Foundation
import XCTest
@testable import NexalMac

final class TransportPresentationTests: XCTestCase {
    private func capability(_ mechanism: TransportMechanism,
                            rdma: [TransportSubsystem: TransportCapability.RDMAState] = [:],
                            reported: String? = "reported text",
                            sourceReporting: Bool = true) -> TransportCapability {
        TransportCapability(rdma: rdma, mechanism: mechanism, reportedText: reported,
                            sourceName: "the Go connector", sourceReporting: sourceReporting)
    }

    private let allMechanisms: [TransportMechanism] = [
        .nativeThunderboltRDMA, .tcpPrivateLAN, .coordinatorMediated, .controlPlaneOnly, .unknown
    ]

    func testFixedProductNamesAreUsedForEachMechanism() {
        XCTAssertEqual(TransportPresentation(capability: capability(.nativeThunderboltRDMA)).label,
                       "Native Thunderbolt RDMA")
        XCTAssertEqual(TransportPresentation(capability: capability(.tcpPrivateLAN)).label,
                       "TCP private-LAN transport")
        XCTAssertEqual(TransportPresentation(capability: capability(.coordinatorMediated)).label,
                       "Relayed through the coordinator")
        XCTAssertEqual(TransportPresentation(capability: capability(.controlPlaneOnly)).label,
                       "No data transport established")
        XCTAssertEqual(TransportPresentation(capability: capability(.unknown)).label,
                       "Transport unknown")
    }

    // The reporting layer's own warnings must survive the move from plain text
    // to an indicator row.
    func testTheReportedStringIsShownVerbatim() {
        let raw = "outbound HTTP(S) private pull prototype — NOT protected by incoming PQ tunnel"
        let state = TransportPresentation(capability: capability(.coordinatorMediated, reported: raw))
        XCTAssertTrue(state.indicator.reason.contains(raw))
        XCTAssertTrue(state.indicator.reason.contains("the Go connector"))
    }

    func testNoReportIsUnknownAndSaysUnknownIsNotUnavailable() {
        let state = TransportPresentation(
            capability: .notReported(sourceName: "the Go connector"))
        XCTAssertEqual(state.mechanism, .unknown)
        XCTAssertEqual(state.indicator.tone, .grey)
        XCTAssertTrue(state.indicator.reason.contains("not the same as unavailable"))
    }

    func testEveryStateCarriesALabelASymbolAReasonAndDetail() {
        for mechanism in allMechanisms {
            let indicator = TransportPresentation(capability: capability(mechanism)).indicator
            XCTAssertEqual(indicator.heading, "Active transport")
            XCTAssertFalse(indicator.label.isEmpty)
            XCTAssertFalse(indicator.systemImage.isEmpty)
            XCTAssertFalse(indicator.reason.isEmpty)
            XCTAssertNotNil(indicator.detail)
        }
    }

    func testEstablishedPathsAreColouredAndNonPathsAreGrey() {
        for mechanism in [TransportMechanism.nativeThunderboltRDMA, .tcpPrivateLAN, .coordinatorMediated] {
            XCTAssertEqual(TransportPresentation(capability: capability(mechanism)).indicator.tone, .colour)
        }
        for mechanism in [TransportMechanism.controlPlaneOnly, .unknown] {
            XCTAssertEqual(TransportPresentation(capability: capability(mechanism)).indicator.tone, .grey)
        }
    }

    func testNoStateClaimsDetectionOrAVendorPerformanceFigure() {
        for mechanism in allMechanisms {
            let indicator = TransportPresentation(capability: capability(mechanism)).indicator
            let text = "\(indicator.label) \(indicator.reason) \(indicator.detail ?? "")"
            XCTAssertFalse(text.contains("Thunderbolt 5 detected"))
            XCTAssertFalse(text.contains("99%"))
            XCTAssertFalse(text.lowercased().contains("reduction in latency"))
        }
    }
}
