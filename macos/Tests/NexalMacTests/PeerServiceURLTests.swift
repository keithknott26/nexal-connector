import XCTest
@testable import NexalMac

final class PeerServiceURLTests: XCTestCase {
    func testPrivateServiceLinks() {
        XCTAssertEqual(PeerServiceURL.make(scheme: "ssh", host: "100.86.1.2")?.absoluteString, "ssh://100.86.1.2")
        XCTAssertEqual(PeerServiceURL.make(scheme: "vnc", host: "fd00::1")?.absoluteString, "vnc://[fd00::1]")
        XCTAssertEqual(PeerServiceURL.make(scheme: "smb", host: "storage.mesh.nexal.systems", share: "Backup #1")?.absoluteString, "smb://storage.mesh.nexal.systems/Backup%20%231")
    }
    func testRejectsInjectedDestinations() {
        for host in ["user@host", "host/path", "host?query", "host#fragment", "bad host", ""] {
            XCTAssertNil(PeerServiceURL.make(scheme: "smb", host: host))
        }
        XCTAssertNil(PeerServiceURL.make(scheme: "https", host: "host"))
    }
}
