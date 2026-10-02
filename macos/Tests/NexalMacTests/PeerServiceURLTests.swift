import XCTest
@testable import NexalMac

final class PeerServiceURLTests: XCTestCase {
    func testPrivateServiceLinks() {
        XCTAssertEqual(PeerServiceURL.make(scheme: "ssh", host: "100.86.1.2")?.absoluteString, "ssh://100.86.1.2")
        XCTAssertEqual(PeerServiceURL.make(scheme: "vnc", host: "fd00::1")?.absoluteString, "vnc://[fd00::1]")
        XCTAssertEqual(PeerServiceURL.make(scheme: "smb", host: "storage.mesh.nexal.systems", share: "Backup #1")?.absoluteString, "smb://storage.mesh.nexal.systems/Backup%20%231")
    }
    func testUsernamePrefilledWithoutPassword() {
        XCTAssertEqual(PeerServiceURL.make(scheme: "vnc", host: "peer.mesh.nexal.systems", user: "keith")?.absoluteString, "vnc://keith@peer.mesh.nexal.systems")
        XCTAssertEqual(PeerServiceURL.make(scheme: "smb", host: "h", user: "a:b@c", share: "S")?.absoluteString, "smb://a%3Ab%40c@h/S")
    }
    func testPasswordEmbeddedEncodedAndRedactable() throws {
        let url = try XCTUnwrap(PeerServiceURL.make(scheme: "smb", host: "p.mesh.nexal.systems", user: "k", password: "p@ss:w/d", share: "S"))
        XCTAssertEqual(url.absoluteString, "smb://k:p%40ss%3Aw%2Fd@p.mesh.nexal.systems/S")
        XCTAssertFalse(PeerServiceURL.redacted(url).contains("p%40ss"))
    }
    func testRejectsInjectedDestinations() {
        for host in ["user@host", "host/path", "host?query", "host#fragment", "bad host", ""] {
            XCTAssertNil(PeerServiceURL.make(scheme: "smb", host: host))
        }
        XCTAssertNil(PeerServiceURL.make(scheme: "https", host: "host"))
    }
}
