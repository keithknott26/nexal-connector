import XCTest
@testable import NexalMac

final class ConnectorFailureTests: XCTestCase {
    func testTimeMachineDiagnosticsBeforeFinalEnvelope() {
        let output = "tmutil: setdestination requires Full Disk Access privileges.\n{\"error\":{\"code\":\"connector_error\",\"message\":\"neXal-Connector needs Full Disk Access\"}}\n"
        XCTAssertEqual(ConnectorFailure.reason(in: Data(output.utf8)), "neXal-Connector needs Full Disk Access")
    }
    func testRawDiagnosticAndEarlierEnvelopesAreNotShown() {
        XCTAssertNil(ConnectorFailure.reason(in: Data("sensitive diagnostic".utf8)))
        XCTAssertNil(ConnectorFailure.reason(in: Data("{\"error\":{\"message\":\"not final\"}}\nraw failure".utf8)))
    }
    func testExistingEnvelopeAndSanitization() {
        XCTAssertEqual(ConnectorFailure.reason(in: Data("{\"error\":{\"message\":\"Permission\\nrequired\"}}\n \n".utf8)), "Permission required")
    }
}
