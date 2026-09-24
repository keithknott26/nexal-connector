import XCTest
@testable import NexalMac

final class LeavePhaseTests: XCTestCase {

    /// Only the two working phases may show a spinner or block another leave.
    func testOnlyWorkingPhasesAreInProgress() {
        XCTAssertTrue(LeavePhase.preparing.inProgress)
        XCTAssertTrue(LeavePhase.leaving.inProgress)
        XCTAssertFalse(LeavePhase.confirming.inProgress)
        XCTAssertFalse(LeavePhase.left.inProgress)
        XCTAssertFalse(LeavePhase.failed(reason: "x").inProgress)
    }

    /// The owner asked to see "preparing to leave" and then "left".
    func testTitlesNameEachStep() {
        XCTAssertEqual(LeavePhase.preparing.title, "Preparing to leave the neXal network\u{2026}")
        XCTAssertEqual(LeavePhase.left.title, "Left the neXal network")
    }

    /// After leaving, the panel is on the pairing screen; the notice must say how to re-pair.
    func testLeftNoticePointsBackToPairing() {
        let detail = LeavePhase.left.detail
        XCTAssertTrue(detail.contains("scan the code"))
        XCTAssertTrue(detail.contains("manual pairing code"))
    }

    /// A failure shows the connector's own reason, not a generic message.
    func testFailureCarriesTheReason() {
        XCTAssertEqual(LeavePhase.failed(reason: "coordinator unreachable").detail, "coordinator unreachable")
        XCTAssertEqual(LeavePhase.failed(reason: "x").severity, .bad)
    }
}
