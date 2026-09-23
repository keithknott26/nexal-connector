import XCTest
@testable import NexalMac

final class SMBPresentationTests: XCTestCase {
    func testDeniedByDefaultWithoutAuthorization() {
        let value = SMBPresentation.derive(nil)
        XCTAssertEqual(value.state, .notAuthorized)
        XCTAssertNil(value.finderAddress)
    }

    func testAuthorizedAvailablePeerGetsFinderAddress() {
        let report = ConnectorStatus.MeshFileSharing(authorized: true, available: true,
                                                     address: "studio.my-network.mesh.nexal.systems", shareName: "Projects", detail: nil)
        XCTAssertEqual(SMBPresentation.derive(report).finderAddress, "smb://studio.my-network.mesh.nexal.systems/Projects")
    }

    func testUpstreamHostnameIsNeverDisplayed() {
        let report = ConnectorStatus.MeshFileSharing(authorized: true, available: true,
                                                     address: "studio.netbird.cloud", shareName: nil, detail: nil)
        XCTAssertNil(SMBPresentation.derive(report).finderAddress)
    }

    func testUnsafeOrUnavailableAddressNeverGetsAnAction() {
        for host in ["evil.example/x", "user@host", "bad host"] {
            let report = ConnectorStatus.MeshFileSharing(authorized: true, available: true,
                                                         address: host, shareName: nil, detail: nil)
            XCTAssertNil(SMBPresentation.derive(report).finderAddress)
        }
    }

    func testDiscoveryNeverClaimsSharedMulticast() {
        let report = ConnectorStatus.MeshDiscovery(wideAreaBonjour: false, gateway: "unavailable",
                                                   bridge: "unavailable", siteId: nil,
                                                   lastRecordAt: nil, detail: nil)
        XCTAssertTrue(DiscoveryPresentation.derive(report).detail.contains("multicast is not routed"))
    }

    func testScreenSharingRequiresPolicyAndPrivateHostname() {
        XCTAssertEqual(ScreenSharingPresentation.derive(nil).state, .notAuthorized)
        let good = ConnectorStatus.MeshScreenSharing(authorized: true, available: true,
            address: "studio.my-network.mesh.nexal.systems", detail: nil)
        XCTAssertEqual(ScreenSharingPresentation.derive(good).address, "vnc://studio.my-network.mesh.nexal.systems")
        let upstream = ConnectorStatus.MeshScreenSharing(authorized: true, available: true,
            address: "studio.netbird.cloud", detail: nil)
        XCTAssertNil(ScreenSharingPresentation.derive(upstream).address)
    }
}
