import Foundation
import XCTest
@testable import NexalMac

final class MeshQuantumPresentationTests: XCTestCase {
    func testOnlyFreshSessionEvidenceEarnsParameterLabel() throws {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let formatter = ISO8601DateFormatter()
        var value: [String: Any] = ["id": "peer", "name": "Mini", "lifecycle": "connected",
            "path": "direct", "pathLabel": "Direct", "pq": "protected",
            "traffic": ["receivedBytes": 0, "sentBytes": 0],
            "quantumProfile": "nexal-mlkem1024-tcp-v2",
            "pqVerifiedAt": formatter.string(from: now.addingTimeInterval(-30)),
            "pqExpiresAt": formatter.string(from: now.addingTimeInterval(150))]
        func label() throws -> String {
            let peer = try JSONDecoder().decode(ConnectorStatus.MeshPeer.self,
                from: JSONSerialization.data(withJSONObject: value))
            return MeshQuantumPresentation.label(peers: [peer], now: now)
        }
        XCTAssertTrue(try label().contains("ML-KEM-1024"))
        value["quantumProfile"] = "nexal-mlkem1024-experimental-v1"
        XCTAssertEqual(try label(), "Not reported")
        value["quantumProfile"] = "nexal-mlkem1024-tcp-v2"
        value["pqExpiresAt"] = formatter.string(from: now)
        XCTAssertEqual(try label(), "Not reported")
        value.removeValue(forKey: "quantumProfile")
        XCTAssertEqual(try label(), "Not reported")
        XCTAssertEqual(MeshQuantumPresentation.label(peers: [], now: now), "Not reported")
    }
}
