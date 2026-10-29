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

    private func peer(_ id: String, pq: String, reason: String?, lifecycle: String = "degraded") -> ConnectorStatus.MeshPeer {
        var fields: [String: Any] = ["id": id, "name": id, "lifecycle": lifecycle, "path": "p2p", "pathLabel": "Direct",
                                     "pq": pq, "traffic": ["receivedBytes": 0, "sentBytes": 0]]
        if let reason { fields["pqReason"] = reason }
        let data = try! JSONSerialization.data(withJSONObject: fields)
        return try! JSONDecoder().decode(ConnectorStatus.MeshPeer.self, from: data)
    }

    func testStuckQuantumSessionIsRenewedOnlyAfterItStaysStuck() {
        var since: [String: Date] = [:]
        let gw = peer("gw-us-east-1", pq: "degraded", reason: "peer-unreachable")
        XCTAssertEqual(AppModel.stuckQuantumPeers([gw], since: &since, now: now), [])
        XCTAssertEqual(AppModel.stuckQuantumPeers([gw], since: &since, now: now.addingTimeInterval(60)), [])
        XCTAssertEqual(AppModel.stuckQuantumPeers([gw], since: &since, now: now.addingTimeInterval(200)), ["gw-us-east-1"])
    }

    func testRecoveredOrUncoveredPeersAreNotStuck() {
        var since: [String: Date] = [:]
        let protected = peer("m2", pq: "protected", reason: nil, lifecycle: "connected")
        let phone = peer("iphone", pq: "unsupported", reason: "peer-lacks-profile")
        let offline = peer("old", pq: "degraded", reason: "peer-unreachable", lifecycle: "offline")
        _ = AppModel.stuckQuantumPeers([protected, phone, offline], since: &since, now: now)
        XCTAssertEqual(AppModel.stuckQuantumPeers([protected, phone, offline], since: &since, now: now.addingTimeInterval(600)), [])
        XCTAssertTrue(since.isEmpty)
    }

    func testStuckClockResetsWhenTheLeaseRecovers() {
        var since: [String: Date] = [:]
        let stuck = peer("gw", pq: "degraded", reason: "evidence-expired")
        let fine = peer("gw", pq: "protected", reason: nil, lifecycle: "connected")
        _ = AppModel.stuckQuantumPeers([stuck], since: &since, now: now)
        _ = AppModel.stuckQuantumPeers([fine], since: &since, now: now.addingTimeInterval(100))
        XCTAssertEqual(AppModel.stuckQuantumPeers([stuck], since: &since, now: now.addingTimeInterval(250)), [])
    }

    func testRenewalIsSkippedWhileAnotherPeerIsWorking() {
        let working = peer("gw", pq: "protected", reason: nil, lifecycle: "connected")
        let sleepingPhone = peer("iphone", pq: "degraded", reason: "session-pending")
        XCTAssertTrue(AppModel.healWouldInterrupt([working, sleepingPhone]))
        XCTAssertFalse(AppModel.healWouldInterrupt([sleepingPhone]))
        XCTAssertFalse(AppModel.healWouldInterrupt([peer("m2", pq: "degraded", reason: "evidence-expired", lifecycle: "connected")]))
    }
}
