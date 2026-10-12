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

final class PeerAddressTextTests: XCTestCase {
    func testLabelsEveryPart() {
        XCTAssertEqual(PeerAddressText.summary(publicIP: "32.211.126.180", lanIP: "192.168.68.52", meshIP: "100.64.1.2", location: "Waterbury, CT, United States"),
                       "Public: 32.211.126.180 · LAN: 192.168.68.52 · Mesh: 100.64.1.2 · Location: Waterbury, CT, United States")
    }
    func testSkipsMissingParts() {
        XCTAssertEqual(PeerAddressText.summary(publicIP: nil, lanIP: "", meshIP: "100.64.1.2", location: nil), "Mesh: 100.64.1.2")
        XCTAssertEqual(PeerAddressText.summary(publicIP: nil, lanIP: nil, meshIP: nil, location: nil), "—")
    }
}
