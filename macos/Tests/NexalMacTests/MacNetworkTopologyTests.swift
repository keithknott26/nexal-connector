import XCTest
@testable import NexalMac

final class MacNetworkTopologyTests: XCTestCase {
    private func peer(_ json: String) throws -> ConnectorStatus.MeshPeer {
        try JSONDecoder().decode(ConnectorStatus.MeshPeer.self, from: Data(json.utf8))
    }
    private func json(id: String, name: String, life: String = "connected", path: String, via: String? = nil, ms: Double? = nil) -> String {
        """
        {"id":"\(id)","name":"\(name)","lifecycle":"\(life)","path":"\(path)","pathLabel":"","pq":"protected",
         "traffic":{"receivedBytes":0,"sentBytes":0}\(via.map { ",\"directVia\":\"\($0)\"" } ?? "")\(ms.map { ",\"latencyMs\":\($0)" } ?? "")}
        """
    }

    func testBuildsSitesSummaryAndValidSVG() throws {
        let peers = [
            try peer(json(id: "a", name: "Studio", path: "direct", via: "lan", ms: 2)),
            try peer(json(id: "b", name: "Laptop", path: "relay", ms: 80)),
            try peer(json(id: "c", name: "gw-1", path: "cloud", ms: 30)),
            try peer(json(id: "d", name: "Old <Mac>", life: "failed", path: "direct"))
        ]
        let t = MacNetworkTopology.build(hostName: "Mini", peers: peers)
        XCTAssertEqual(t.summary.total, 4)
        XCTAssertEqual(t.summary.online, 3)
        XCTAssertEqual(t.summary.lan, 1)
        XCTAssertEqual(t.summary.relayed, 2)
        XCTAssertEqual(t.summary.offline, 1)
        XCTAssertNotNil(t.nodes.first { $0.id == "relay" })
        let svg = t.svg()
        XCTAssertTrue(svg.hasPrefix("<svg"))
        XCTAssertTrue(svg.hasSuffix("</svg>"))
        XCTAssertFalse(svg.contains("Old <Mac>"))
        XCTAssertTrue(svg.contains("Old &lt;Mac&gt;"))
        XCTAssertFalse(t.svg(animated: false).contains("animateMotion"))
    }

    func testNoPeersStillDrawsThisMac() {
        let t = MacNetworkTopology.build(hostName: "", peers: [])
        XCTAssertNotNil(t.nodes.first { $0.id == "this-mac" })
        XCTAssertNil(t.nodes.first { $0.id == "relay" })
    }
}
