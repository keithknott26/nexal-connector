import XCTest
@testable import NexalMac
final class AccountPortalTests: XCTestCase {
    func testAccountPortalUsesOnlyTheSelectedHTTPSOrigin() {
        XCTAssertEqual(AccountPortal.url(coordinator: "https://coordinator.example")?.absoluteString, "https://coordinator.example/#/account")
        for value in ["http://coordinator.example", "https://user:password@coordinator.example", "https://coordinator.example/other", "https://coordinator.example?redirect=other", "https://coordinator.example/#other"] {
            XCTAssertNil(AccountPortal.url(coordinator: value))
        }
    }
}
