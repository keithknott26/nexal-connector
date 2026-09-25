import XCTest
@testable import NexalMac

final class FeatureFlagsTests: XCTestCase {
    func testMissingFlagsFailClosed() throws {
        let flags = try JSONDecoder().decode(ConnectorFeatures.self, from: Data("{}".utf8))
        XCTAssertEqual(flags, ConnectorFeatures())
    }

    func testKnownFlagsDecodeAndUnknownFlagsAreIgnored() throws {
        let json = #"{"remote_ssh":true,"remote_vnc":true,"chat":true,"future":true}"#
        let flags = try JSONDecoder().decode(ConnectorFeatures.self, from: Data(json.utf8))
        XCTAssertTrue(flags.remoteSSH)
        XCTAssertTrue(flags.remoteVNC)
        XCTAssertTrue(flags.chat)
        XCTAssertFalse(flags.networkFiles)
        XCTAssertFalse(flags.wakeOnLAN)
        XCTAssertFalse(flags.videoConferencing)
    }
}
