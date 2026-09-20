import Foundation
import XCTest
@testable import NexalMac

final class RDMAPresentationTests: XCTestCase {
    private func capability(_ rdma: [TransportSubsystem: TransportCapability.RDMAState],
                            mechanism: TransportMechanism = .unknown,
                            sourceReporting: Bool = true) -> TransportCapability {
        TransportCapability(rdma: rdma, mechanism: mechanism, reportedText: nil,
                            sourceName: "the Go connector", sourceReporting: sourceReporting)
    }

    // Unknown is the state today and until reporting layers exist. It must be
    // reachable with no source at all, and must not read as a fault.
    func testUnknownIsTheDefaultWhenNothingIsReporting() {
        let state = RDMAPresentation(capability: .notReported(sourceName: "the Go connector"))
        XCTAssertEqual(state.summary, .unknown)
        XCTAssertEqual(state.indicator.tone, .grey)
        XCTAssertTrue(state.reason.contains("No layer is reporting"))
        XCTAssertTrue(state.reason.contains(RDMAPresentation.speedNotCapability))
    }

    // §29.8: the two facts have different owners and must never be one flag.
    func testSubsystemsAreIndependentAndBothDefaultToUnknown() {
        let capability = TransportCapability.notReported(sourceName: "the Go connector")
        for subsystem in TransportSubsystem.allCases {
            XCTAssertEqual(capability.rdmaState(for: subsystem), .unknown)
        }
        XCTAssertEqual(capability.subsystems(in: .unknown).count, TransportSubsystem.allCases.count)
        XCTAssertTrue(TransportSubsystem.allCases.contains(.computeCollectives))
        XCTAssertTrue(TransportSubsystem.allCases.contains(.memoryPager))
    }

    func testOneSubsystemAvailableDoesNotClaimTheOther() {
        let state = RDMAPresentation(capability: capability([.computeCollectives: .available]))
        XCTAssertEqual(state.summary, .availableForSome)
        XCTAssertEqual(state.capability.rdmaState(for: .memoryPager), .unknown)
        XCTAssertTrue(state.label.contains("some paths"))
        XCTAssertTrue(state.reason.contains("Compute collectives"))
        XCTAssertTrue(state.reason.contains(RDMAPresentation.speedNotCapability))
    }

    func testTheOtherDirectionIsSymmetric() {
        let state = RDMAPresentation(capability: capability([.memoryPager: .available]))
        XCTAssertEqual(state.summary, .availableForSome)
        XCTAssertEqual(state.capability.rdmaState(for: .computeCollectives), .unknown)
        XCTAssertTrue(state.reason.contains("Memory pager"))
    }

    func testBothAvailableIsTheOnlyRouteToAvailableForAll() {
        let state = RDMAPresentation(
            capability: capability([.computeCollectives: .available, .memoryPager: .available],
                                   mechanism: .nativeThunderboltRDMA))
        XCTAssertEqual(state.summary, .availableForAll)
        XCTAssertEqual(state.label, "Available on this Mac")
        XCTAssertEqual(state.indicator.tone, .colour)
    }

    func testAllUnavailableIsDistinctFromUnknown() {
        let unavailable = RDMAPresentation(
            capability: capability([.computeCollectives: .unavailable, .memoryPager: .unavailable]))
        XCTAssertEqual(unavailable.summary, .unavailableForAll)
        XCTAssertEqual(unavailable.indicator.tone, .grey)
        let mixed = RDMAPresentation(capability: capability([.memoryPager: .unavailable]))
        XCTAssertEqual(mixed.summary, .unknown, "one unreported path keeps the row unknown")
    }

    func testEveryGreySummarySaysSharingContinuesAtTCPIPSpeed() {
        let greys = [RDMAPresentation(capability: .notReported(sourceName: "x")),
                     RDMAPresentation(capability: capability([.memoryPager: .unavailable])),
                     RDMAPresentation(capability: capability([.computeCollectives: .unavailable,
                                                             .memoryPager: .unavailable]))]
        for state in greys {
            XCTAssertEqual(state.indicator.tone, .grey)
            XCTAssertTrue(state.label.contains("TCP/IP speed"))
            XCTAssertTrue(state.reason.contains(RDMAPresentation.speedNotCapability))
        }
    }

    // The breakdown always lists every path, so a state is never shown without
    // saying which path it belongs to.
    func testBreakdownAlwaysNamesEveryPathAndItsState() {
        let state = RDMAPresentation(capability: capability([.computeCollectives: .available,
                                                            .memoryPager: .unavailable]))
        XCTAssertEqual(state.breakdown.count, TransportSubsystem.allCases.count)
        XCTAssertEqual(state.indicator.detailLines, state.breakdown)
        XCTAssertTrue(state.breakdown.contains { $0.contains("Compute collectives")
            && $0.contains("reported available") })
        XCTAssertTrue(state.breakdown.contains { $0.contains("Memory pager")
            && $0.contains("not available here") })
    }

    func testBreakdownExplainsThatThePathsCannotShareOneRDMA() {
        let state = RDMAPresentation(capability: .notReported(sourceName: "x"))
        let text = state.breakdown.joined(separator: " ")
        XCTAssertTrue(text.contains("cannot reuse the collective backend"))
        XCTAssertTrue(state.explanation.contains("RDMA is per path, not one setting."))
        for line in state.breakdown {
            XCTAssertTrue(line.contains("unknown, not reported yet"))
        }
    }

    // A grey RDMA row is the normal row: no failure language, no upsell, and no
    // claim that this app performs or measures RDMA itself (§26.6, §29.8).
    func testGreyCopyHasNoFailureLanguageNoUpsellAndNoOwnershipClaim() {
        let greys = [RDMAPresentation(capability: .notReported(sourceName: "the Go connector")),
                     RDMAPresentation(capability: capability([.memoryPager: .unavailable])),
                     RDMAPresentation(capability: capability([.computeCollectives: .unavailable,
                                                             .memoryPager: .unavailable]))]
        let forbidden = ["error", "failed", "failure", "problem", "broken", "warning",
                         "upgrade", "buy ", "purchase", "thunderbolt 5 detected",
                         "nexal probes", "we measure", "99%", "reduction in latency",
                         "300 microseconds", "3 microseconds"]
        for state in greys {
            XCTAssertNotEqual(state.summary, .availableForAll)
            let text = ("\(state.label) \(state.reason) \(state.explanation) "
                        + state.breakdown.joined(separator: " ")).lowercased()
            for word in forbidden {
                XCTAssertFalse(text.contains(word), "\(word) in: \(text)")
            }
        }
    }

    func testEverySummaryStatesTheDocumentedRequirementAndThePerPathRule() {
        let states: [RDMAPresentation] = [
            RDMAPresentation(capability: capability([.computeCollectives: .available,
                                                     .memoryPager: .available])),
            RDMAPresentation(capability: capability([.computeCollectives: .available])),
            RDMAPresentation(capability: capability([.computeCollectives: .unavailable,
                                                     .memoryPager: .unavailable])),
            RDMAPresentation(capability: .notReported(sourceName: "x"))]
        for state in states {
            XCTAssertTrue(state.explanation.contains("Thunderbolt 5"))
            XCTAssertTrue(state.explanation.contains("Apple silicon"))
            XCTAssertTrue(state.explanation.contains("macOS 26.2"))
            XCTAssertTrue(state.explanation.contains("RDMA is per path"))
            XCTAssertEqual(state.indicator.heading, "Thunderbolt RDMA")
            XCTAssertFalse(state.indicator.systemImage.isEmpty)
            XCTAssertFalse(state.indicator.label.isEmpty)
        }
    }

    // The two indicators answer different questions and never merge (§26.3).
    func testRDMAAndResourceSharingRemainSeparateIndicators() throws {
        let paused = try ConnectorStatus.decode(Data(#"{"paused":true}"#.utf8))
        let sharing = ResourceSharingPresentation(status: paused)
        let rdma = RDMAPresentation(capability: ConnectorStatusCapabilitySource().capability(from: paused))
        XCTAssertEqual(sharing.state, .pausedByOwner)
        XCTAssertEqual(rdma.summary, .unknown)
        XCTAssertNotEqual(rdma.indicator.heading, sharing.indicator.heading)
    }
}
