import XCTest
@testable import NexalMac

final class MeshRuntimeCheckTests: XCTestCase {
    func testGateParsing() {
        XCTAssertEqual(MeshRuntimeCheck.gate(from: "0.79.0-nexal-mlkem1024-gated.11-mac"), 11)
        XCTAssertEqual(MeshRuntimeCheck.gate(from: "0.79.0-nexal-mlkem1024-gated.13-linux"), 13)
        XCTAssertNil(MeshRuntimeCheck.gate(from: "0.79.0"))
    }

    func testOutdatedOnlyWhenOlder() {
        XCTAssertTrue(MeshRuntimeCheck.isOutdated(installed: "0.79.0-nexal-mlkem1024-gated.11-mac"))
        XCTAssertFalse(MeshRuntimeCheck.isOutdated(installed: "0.79.0-nexal-mlkem1024-gated.13-mac"))
        XCTAssertFalse(MeshRuntimeCheck.isOutdated(installed: "0.79.0-nexal-mlkem1024-gated.14-mac"))
        XCTAssertFalse(MeshRuntimeCheck.isOutdated(installed: nil), "unknown must not warn")
        XCTAssertFalse(MeshRuntimeCheck.isOutdated(installed: "unknown"))
    }

    /// The constant must match the version the build scripts use.
    func testExpectedMatchesRuntimeVersionFile() throws {
        let file = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("experiments/mlkem1024-mesh/RUNTIME_VERSION")
        guard let text = try? String(contentsOf: file, encoding: .utf8) else { throw XCTSkip("RUNTIME_VERSION not in this checkout") }
        XCTAssertEqual(MeshRuntimeCheck.expectedVersion, text.trimmingCharacters(in: .whitespacesAndNewlines))
    }
}
