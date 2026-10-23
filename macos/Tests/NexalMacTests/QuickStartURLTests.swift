import XCTest
@testable import NexalMac

@MainActor
final class QuickStartURLTests: XCTestCase {
    func testListsMeshThenLANThenLoopback() {
        let extra = InferenceQuickStart.extraAddresses(mesh: "100.86.56.136", lan: "192.168.68.58")
        XCTAssertEqual(InferenceQuickStart.urls(port: 8001, extra: extra).map(\.url),
                       ["http://100.86.56.136:8001/v1", "http://192.168.68.58:8001/v1", "http://127.0.0.1:8001/v1"])
    }

    func testSkipsUnknownOrInvalidAddresses() {
        XCTAssertTrue(InferenceQuickStart.extraAddresses(mesh: nil, lan: "fe80::1").isEmpty)
        XCTAssertTrue(InferenceQuickStart.extraAddresses(mesh: "999.1.1.1", lan: "127.0.0.1").isEmpty)
        XCTAssertEqual(InferenceQuickStart.urls(port: 8090, extra: []).map(\.url), ["http://127.0.0.1:8090/v1"])
    }
}
