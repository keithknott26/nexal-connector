import XCTest
@testable import NexalMac

@MainActor
final class MeshHealTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_000_000)

    func testWaitsBeforeJoiningAgain() {
        XCTAssertFalse(AppModel.shouldHealMesh(downSince: now.addingTimeInterval(-10), lastHeal: nil, now: now))
        XCTAssertTrue(AppModel.shouldHealMesh(downSince: now.addingTimeInterval(-60), lastHeal: nil, now: now))
    }

    func testDoesNothingWhileTheNetworkIsUp() {
        XCTAssertFalse(AppModel.shouldHealMesh(downSince: nil, lastHeal: nil, now: now))
    }

    func testDoesNotRepeatWithinTheCooldown() {
        let down = now.addingTimeInterval(-600)
        XCTAssertFalse(AppModel.shouldHealMesh(downSince: down, lastHeal: now.addingTimeInterval(-30), now: now))
        XCTAssertTrue(AppModel.shouldHealMesh(downSince: down, lastHeal: now.addingTimeInterval(-200), now: now))
    }

    func testStuckServiceIsRecognised() {
        XCTAssertTrue(AppModel.serviceNeedsReinstall("daemon up failed: ... Unavailable desc = update settings are disabled, you cannot ..."))
        XCTAssertFalse(AppModel.serviceNeedsReinstall("connection refused"))
    }
}
