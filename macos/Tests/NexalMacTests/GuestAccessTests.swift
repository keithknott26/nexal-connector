import XCTest
@testable import NexalMac

final class GuestAccessTests: XCTestCase {
    func testInviteCodeTravelsOnStdinOnly() {
        let config = URL(fileURLWithPath: "/private/tmp/config.json")
        XCTAssertEqual(CLICommand.redeemGuestInvitation.arguments(config: config), ["guest", "redeem", "--code-stdin", "--config", config.path])
        XCTAssertEqual(CLICommand.activateGuestInvitation.arguments(config: config), ["guest", "activate", "--config", config.path])
        XCTAssertGreaterThan(CLICommand.redeemGuestInvitation.timeLimit, 40)
    }
    func testPersistedExpiryAndClockRollbackFailClosed() throws {
        let record = GuestAccessRecord(grantId: "g", accessExpiresAt: "2026-09-26T15:00:00Z", receivedAt: "2026-09-26T14:00:00Z", inviterEmail: "owner@example.com", expired: false)
        XCTAssertFalse(record.isExpired(at: try XCTUnwrap(GuestAccessRecord.parse("2026-09-26T14:59:59Z"))))
        XCTAssertTrue(record.isExpired(at: try XCTUnwrap(GuestAccessRecord.parse("2026-09-26T15:00:00Z"))))
        XCTAssertTrue(record.isExpired(at: try XCTUnwrap(GuestAccessRecord.parse("2026-09-26T13:00:00Z"))))
        XCTAssertTrue(GuestAccessRecord(grantId: "g", accessExpiresAt: "invalid", receivedAt: "invalid", inviterEmail: "owner@example.com", expired: false).isExpired())
    }
}
