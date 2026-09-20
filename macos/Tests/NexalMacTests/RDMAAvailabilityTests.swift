import Foundation
import XCTest
@testable import NexalMac

final class RDMAAvailabilityTests: XCTestCase {
    func testSupportedMacOSWithoutAProbedLinkIsUnknownNotAvailable() {
        let state = RDMAAvailability(osMajor: 26, osMinor: 2,
                                     isAppleSiliconBuild: true, establishedLink: false)
        XCTAssertEqual(state.state, .unknown)
        XCTAssertEqual(state.indicator.tone, .grey)
    }

    func testEstablishedLinkIsTheOnlyRouteToAvailable() {
        let state = RDMAAvailability(osMajor: 26, osMinor: 2,
                                     isAppleSiliconBuild: true, establishedLink: true)
        XCTAssertEqual(state.state, .available)
        XCTAssertEqual(state.indicator.tone, .colour)
    }

    // The founder's Macs run an OS below 26.2, so this is the ordinary state.
    func testOlderMacOSIsDefinitelyUnavailableAndSaysSharingContinues() {
        for (major, minor) in [(14, 0), (15, 6), (26, 1)] {
            let state = RDMAAvailability(osMajor: major, osMinor: minor,
                                         isAppleSiliconBuild: true, establishedLink: false)
            XCTAssertEqual(state.state, .unavailable)
            XCTAssertTrue(state.reason.contains("TCP/IP"))
            XCTAssertTrue(state.reason.contains(RDMAAvailability.speedNotCapability))
            XCTAssertTrue(state.explanation.contains("macOS 26.2"))
        }
    }

    func testNonAppleSiliconBuildIsUnknownRatherThanAsserted() {
        let state = RDMAAvailability(osMajor: 26, osMinor: 3,
                                     isAppleSiliconBuild: false, establishedLink: false)
        XCTAssertEqual(state.state, .unknown)
    }

    // A grey RDMA row is the normal row. It must not read as an error, and it
    // must never suggest different hardware (§26.6).
    func testGreyStatesNeverReadAsFailureAndNeverUpsell() {
        let greys = [RDMAAvailability(osMajor: 14, osMinor: 0, isAppleSiliconBuild: true, establishedLink: false),
                     RDMAAvailability(osMajor: 26, osMinor: 2, isAppleSiliconBuild: true, establishedLink: false),
                     RDMAAvailability(osMajor: 26, osMinor: 2, isAppleSiliconBuild: false, establishedLink: false)]
        let forbidden = ["error", "failed", "failure", "problem", "broken", "warning",
                         "upgrade", "buy", "M4 Pro", "purchase", "Thunderbolt 5 detected"]
        for state in greys {
            XCTAssertNotEqual(state.state, .available)
            let text = "\(state.label) \(state.reason) \(state.explanation)".lowercased()
            for word in forbidden {
                XCTAssertFalse(text.contains(word.lowercased()), "\(word) in: \(text)")
            }
            XCTAssertTrue(state.label.contains("TCP/IP speed"))
        }
    }

    func testEveryStateStatesTheRealRequirement() {
        for link in [true, false] {
            let state = RDMAAvailability(osMajor: 26, osMinor: 2,
                                         isAppleSiliconBuild: true, establishedLink: link)
            XCTAssertTrue(state.explanation.contains("Thunderbolt 5"))
            XCTAssertTrue(state.explanation.contains("Apple silicon"))
            XCTAssertEqual(state.indicator.heading, "Thunderbolt RDMA")
            XCTAssertFalse(state.indicator.systemImage.isEmpty)
        }
    }

    func testProbeReadsTheRunningSystemAndNeverThrows() {
        let probed = RDMAAvailability.probe()
        XCTAssertFalse(probed.reason.isEmpty)
        // Without a reported link, a probe can never conclude "available".
        XCTAssertNotEqual(probed.state, .available)
    }
}
