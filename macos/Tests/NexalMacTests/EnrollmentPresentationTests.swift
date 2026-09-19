import Foundation
import XCTest
@testable import NexalMac

final class EnrollmentPresentationTests: XCTestCase {
    private let preview = URL(fileURLWithPath: "/Users/test/Nexal-Local-Preview/config.json")
    private let production = URL(fileURLWithPath: "/Users/test/Nexal/config.json")

    func testInitialStateDoesNotClaimEnrollment() {
        let state = EnrollmentPresentation()
        XCTAssertFalse(state.showsConfirmation(for: preview))
    }

    func testSuccessShowsConfirmationOnlyForEnrolledConfiguration() {
        var state = EnrollmentPresentation()
        state.recordSuccess(for: preview)
        XCTAssertTrue(state.showsConfirmation(for: preview))
        XCTAssertFalse(state.showsConfirmation(for: production))
    }

    func testStatusRestoresConfirmationWithoutInvitationCode() {
        var state = EnrollmentPresentation()
        state.observeHost("host_123", for: preview)
        XCTAssertTrue(state.showsConfirmation(for: preview))
        XCTAssertFalse(state.showsConfirmation(for: production))
    }

    func testMissingOrEmptyHostDoesNotConfirmEnrollment() {
        var state = EnrollmentPresentation()
        for host in [nil, "", " \n"] as [String?] {
            state.observeHost(host, for: preview)
            XCTAssertFalse(state.showsConfirmation(for: preview))
        }
    }

    func testRefreshDoesNotHideReplacementInput() {
        var state = EnrollmentPresentation()
        state.recordSuccess(for: preview)
        state.beginReplacement(for: preview)
        state.observeHost("host_existing", for: preview)
        XCTAssertFalse(state.showsConfirmation(for: preview))
    }

    func testReplacementSuccessRestoresConfirmation() {
        var state = EnrollmentPresentation()
        state.recordSuccess(for: preview)
        state.beginReplacement(for: preview)
        state.recordSuccess(for: preview)
        XCTAssertTrue(state.showsConfirmation(for: preview))
    }

    func testReplacementDoesNotAffectOtherConfiguration() {
        var state = EnrollmentPresentation()
        state.recordSuccess(for: preview)
        state.recordSuccess(for: production)
        state.beginReplacement(for: preview)
        XCTAssertFalse(state.showsConfirmation(for: preview))
        XCTAssertTrue(state.showsConfirmation(for: production))
    }

    func testEquivalentConfigurationPathsShareState() {
        var state = EnrollmentPresentation()
        state.recordSuccess(for: preview)
        let equivalent = preview.deletingLastPathComponent().appendingPathComponent("./config.json")
        XCTAssertTrue(state.showsConfirmation(for: equivalent))
    }
}
