import XCTest

final class ConnectorUIContractTests: XCTestCase {
    private var source: String {
        get throws {
            let tests = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            let panel = tests.deletingLastPathComponent().deletingLastPathComponent()
                .appendingPathComponent("Sources/NexalMac/NetworkPanel.swift")
            return try String(contentsOf: panel, encoding: .utf8)
        }
    }

    func testCustomerFlowContainsOnlyTheRequiredPairingSteps() throws {
        let text = try source
        XCTAssertTrue(text.contains("sign in with Apple"))
        XCTAssertTrue(text.contains("Show pairing code"))
        XCTAssertTrue(text.contains("PAIR MANUALLY"))
        XCTAssertTrue(text.contains("tap Pair manually"))
        XCTAssertTrue(text.contains("Connected computers"))
        XCTAssertTrue(text.contains("Activity graphs"))
        XCTAssertTrue(text.contains("Post-quantum protection"))
        XCTAssertTrue(text.contains("Cloudflare route"))
        XCTAssertTrue(text.contains("P2P — direct"))
        XCTAssertTrue(text.contains("Leave neXal network"))
        XCTAssertTrue(text.contains("metered"))
    }

    func testImplementationDefaultsAreNotCustomerControls() throws {
        let text = try source
        for forbidden in [
            "Contribute private resources", "Use bundled nexal", "Choose installed",
            "Use development environment", "Memory limit", "Keep for owner",
            "I approve private-only enrollment", "accept-jobs-now", "Picker(", "Toggle("
        ] {
            XCTAssertFalse(text.contains(forbidden), "Customer UI still exposes \(forbidden)")
        }
    }
}
