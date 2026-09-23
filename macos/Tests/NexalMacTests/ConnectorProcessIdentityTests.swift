import XCTest
@testable import NexalMac

final class ConnectorProcessIdentityTests: XCTestCase {
    func testPersistedHostIdentityRequiresANonEmptyHostID() throws {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let config = directory.appendingPathComponent("config.json")

        XCTAssertFalse(ConnectorProcess.hasPersistedHostIdentity(at: config))
        try Data(#"{"version":1,"hostId":null,"credential":"must-not-matter"}"#.utf8).write(to: config)
        XCTAssertFalse(ConnectorProcess.hasPersistedHostIdentity(at: config))
        try Data(#"{"version":1,"hostId":"   "}"#.utf8).write(to: config)
        XCTAssertFalse(ConnectorProcess.hasPersistedHostIdentity(at: config))
        try Data(#"{"version":1,"hostId":"host-authorized","unknown":true}"#.utf8).write(to: config)
        XCTAssertTrue(ConnectorProcess.hasPersistedHostIdentity(at: config))
    }
}
