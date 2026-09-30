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
        XCTAssertTrue(text.contains("Pair manually"))
        XCTAssertTrue(text.contains("tap Pair manually"))
        XCTAssertTrue(text.contains("Your connections"))
        XCTAssertTrue(text.contains("Activity graphs"))
        XCTAssertTrue(text.contains("Post-quantum protection"))
        XCTAssertTrue(text.contains("neXal cloud route"))
        XCTAssertTrue(text.contains("pathLabel.isEmpty ? \"Direct\""))
        XCTAssertTrue(text.contains("Leave neXal network"))
        XCTAssertTrue(text.contains("metered"))
    }

    // Quantum evidence behavior is covered by MeshQuantumPresentationTests.

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

    /// The exit-node checkbox is the one intended customer switch. It lives in
    /// its own file so the rule above stays absolute for NetworkPanel, and this
    /// test pins that file to exactly that one control.
    func testTheExitNodeCheckboxIsTheOnlyCustomerSwitch() throws {
        let file = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("Sources/NexalMac/ExitNodeCheckbox.swift")
        let text = try String(contentsOf: file, encoding: .utf8)
        XCTAssertEqual(text.components(separatedBy: "Toggle(").count - 1, 1, "exactly one switch")
        XCTAssertTrue(text.contains("Route all of my internet traffic through this exit node"))
        XCTAssertFalse(text.contains("Picker("))
        XCTAssertTrue(try source.contains("ExitNodeCheckbox("), "the panel uses the dedicated checkbox")
    }
}
