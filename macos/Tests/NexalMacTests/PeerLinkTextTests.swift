import XCTest
@testable import NexalMac

final class PeerLinkTextTests: XCTestCase {
    func testLatencyThenBandwidth() {
        XCTAssertEqual(PeerLinkText.summary(direct: true, latencyMs: 81, bandwidthMbps: 104.4), "⚡️ 81 ms @ 104 Mbps")
        XCTAssertEqual(PeerLinkText.summary(direct: false, latencyMs: 9, bandwidthMbps: 9.46), "9 ms @ 9.5 Mbps")
        XCTAssertEqual(PeerLinkText.summary(direct: true, latencyMs: 1, bandwidthMbps: 2400), "⚡️ 1 ms @ 2.4 Gbps")
    }

    func testNoBandwidthDropsTheAtSign() {
        XCTAssertEqual(PeerLinkText.summary(direct: true, latencyMs: 81, bandwidthMbps: nil), "⚡️ 81 ms")
        XCTAssertEqual(PeerLinkText.summary(direct: true, latencyMs: 81, bandwidthMbps: 0), "⚡️ 81 ms")
        XCTAssertEqual(PeerLinkText.summary(direct: false, latencyMs: 45, bandwidthMbps: .nan), "45 ms")
    }
}
