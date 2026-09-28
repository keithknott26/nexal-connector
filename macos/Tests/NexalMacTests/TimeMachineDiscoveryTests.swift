import XCTest
@testable import NexalMac

final class TimeMachineDiscoveryTests: XCTestCase {
    @MainActor func testOnlyAssignedReadyPrivateDestinationIsPublished() throws {
        func state(_ changes: [String: Any] = [:]) throws -> TimeMachineReport.State {
            var fields: [String: Any] = ["role": "client", "state": "ready", "serviceState": "ready",
                                        "host": "gw-us-east-1.mesh.nexal.systems", "share": "tm0123456789abcdef0123"]
            fields.merge(changes) { _, new in new }
            return try TimeMachineReport.decode(JSONSerialization.data(withJSONObject: ["timeMachine": fields])).timeMachine
        }
        XCTAssertTrue(TimeMachineDiscovery.eligible(try state(), connected: true))
        XCTAssertTrue(TimeMachineDiscovery.eligible(try state(["host": "100.86.173.7"]), connected: true))
        XCTAssertFalse(TimeMachineDiscovery.eligible(try state(["host": "15.204.217.213"]), connected: true))
        XCTAssertFalse(TimeMachineDiscovery.eligible(try state(), connected: false))
        XCTAssertFalse(TimeMachineDiscovery.eligible(nil, connected: true))
        for changes: [String: Any] in [["enabled": false], ["entitled": false], ["serviceState": "provisioning"],
                                       ["host": "example.com"], ["share": "other,adVF=0x82"]] {
            XCTAssertFalse(TimeMachineDiscovery.eligible(try state(changes), connected: true))
        }
    }
}
