import Foundation
import XCTest
@testable import NexalMac

final class PeerSmoothingTests: XCTestCase {
    private func status(_ peers: [(id: String, lifecycle: String, latency: Double?)]) throws -> ConnectorStatus {
        let list = peers.map { p in
            """
            {"id":"\(p.id)","name":"\(p.id)","lifecycle":"\(p.lifecycle)","path":"relay","pathLabel":"relay",
             "pq":"protected","traffic":{"receivedBytes":0,"sentBytes":0}\(p.latency.map { ",\"latencyMs\":\($0)" } ?? "")}
            """
        }.joined(separator: ",")
        let json = #"{"paused":false,"mesh":{"providerAvailable":true,"lifecycle":"connected","pq":"protected","peers":["# + list + "]}}"
        return try JSONDecoder().decode(ConnectorStatus.self, from: Data(json.utf8))
    }

    func testABriefDropKeepsTheLastStateAndLatency() throws {
        var smoother = PeerSmoother()
        let t0 = Date()
        _ = smoother.smooth(try status([("mini", "connected", 40)]), now: t0)
        let blip = smoother.smooth(try status([("mini", "connecting", nil)]), now: t0.addingTimeInterval(5))
        XCTAssertEqual(blip.mesh?.peers.first?.lifecycle, "connected")
        XCTAssertEqual(blip.mesh?.peers.first?.latencyMs, 40)
        // A drop that lasts beyond the grace period is shown.
        let real = smoother.smooth(try status([("mini", "connecting", nil)]), now: t0.addingTimeInterval(PeerSmoother.connectedGrace + 1))
        XCTAssertEqual(real.mesh?.peers.first?.lifecycle, "connecting")
    }

    func testAPeerMissingFromOnePollIsKept() throws {
        var smoother = PeerSmoother()
        let t0 = Date()
        _ = smoother.smooth(try status([("a", "connected", 10), ("b", "connected", 20)]), now: t0)
        let next = smoother.smooth(try status([("a", "connected", 10)]), now: t0.addingTimeInterval(5))
        XCTAssertEqual(next.mesh?.peers.map(\.id), ["a", "b"])
    }

    func testMeasuredLatencyFillsARelayedPeer() throws {
        var smoother = PeerSmoother()
        smoother.measured["mini"] = (55, Date())
        let out = smoother.smooth(try status([("mini", "connected", 0)]))
        XCTAssertEqual(out.mesh?.peers.first?.latencyMs, 55)
    }
}
