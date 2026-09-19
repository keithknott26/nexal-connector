import Foundation
import XCTest
@testable import NexalMac

final class ManualAcceptancePresentationTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_000)

    private func status(paused: Bool = false, supported: Bool = true,
                        overrideEnabled: Bool = true, until: String = "1970-01-01T00:20:00.000Z") throws -> ConnectorStatus {
        let data = try JSONSerialization.data(withJSONObject: [
            "paused": paused, "hostId": "host-test", "manualAcceptanceSupported": supported,
            "ownerActivityOverride": overrideEnabled, "acceptJobsUntil": until
        ])
        return try ConnectorStatus.decode(data)
    }

    func testConfirmedWindowShowsActiveButton() throws {
        let state = ManualAcceptancePresentation(status: try status(), now: now)
        XCTAssertTrue(state.isActive)
        XCTAssertEqual(state.buttonTitle, "Accepting private jobs")
        XCTAssertEqual(state.activeUntil, Date(timeIntervalSince1970: 1_200))
    }

    func testMissingStatusNeverClaimsPermission() {
        let state = ManualAcceptancePresentation(status: nil, now: now)
        XCTAssertFalse(state.isActive)
        XCTAssertEqual(state.buttonTitle, "Accept jobs now")
    }

    func testPauseOrMissingConsentClearsConfirmation() throws {
        for value in [try status(paused: true), try status(supported: false), try status(overrideEnabled: false)] {
            XCTAssertFalse(ManualAcceptancePresentation(status: value, now: now).isActive)
        }
    }

    func testExpiredMalformedAndMissingDeadlinesFailClosed() throws {
        for raw in ["1970-01-01T00:16:40.000Z", "1970-01-01T00:00:00Z", "", "not-a-date"] {
            XCTAssertFalse(ManualAcceptancePresentation(status: try status(until: raw), now: now).isActive)
        }
        let missing = try ConnectorStatus.decode(Data(#"{"paused":false,"manualAcceptanceSupported":true,"ownerActivityOverride":true}"#.utf8))
        XCTAssertFalse(ManualAcceptancePresentation(status: missing, now: now).isActive)
    }

    func testWholeSecondDeadlineIsSupported() throws {
        XCTAssertTrue(ManualAcceptancePresentation(
            status: try status(until: "1970-01-01T00:20:00Z"), now: now).isActive)
    }

    func testSetupRequirementsExplainDisabledButton() throws {
        let valid = try status()
        XCTAssertNotNil(ManualAcceptancePresentation.unavailableReason(
            localPreview: false, hasExecutable: true, configurationExists: true, status: valid))
        XCTAssertNotNil(ManualAcceptancePresentation.unavailableReason(
            localPreview: true, hasExecutable: false, configurationExists: true, status: valid))
        XCTAssertNotNil(ManualAcceptancePresentation.unavailableReason(
            localPreview: true, hasExecutable: true, configurationExists: false, status: valid))
        XCTAssertNil(ManualAcceptancePresentation.unavailableReason(
            localPreview: true, hasExecutable: true, configurationExists: true, status: nil))
    }

    func testOldAndUnenrolledConnectorsAreNotOfferedAcceptance() throws {
        let old = try ConnectorStatus.decode(Data(#"{"paused":true,"hostId":"host-test"}"#.utf8))
        let unenrolled = try ConnectorStatus.decode(Data(#"{"paused":true,"manualAcceptanceSupported":true}"#.utf8))
        for value in [old, unenrolled] {
            XCTAssertNotNil(ManualAcceptancePresentation.unavailableReason(
                localPreview: true, hasExecutable: true, configurationExists: true, status: value))
        }
        XCTAssertNil(ManualAcceptancePresentation.unavailableReason(
            localPreview: true, hasExecutable: true, configurationExists: true, status: try status()))
    }
}
