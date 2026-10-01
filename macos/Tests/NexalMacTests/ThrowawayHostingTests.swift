import Foundation
import XCTest
@testable import NexalMac

final class ThrowawayHostingTests: XCTestCase {
    func testSandboxArguments() {
        let config = URL(fileURLWithPath: "/tmp/c.json")
        XCTAssertEqual(CLICommand.sandbox(action: "list", id: nil, kind: nil).arguments(config: config),
                       ["sandbox", "--action", "list", "--config", config.path])
        XCTAssertEqual(CLICommand.sandbox(action: "connect", id: "sb1", kind: "vnc").arguments(config: config),
                       ["sandbox", "--action", "connect", "--id", "sb1", "--kind", "vnc", "--config", config.path])
        XCTAssertEqual(CLICommand.sandbox(action: "connect", id: "sb1", kind: "ssh").arguments(config: config),
                       ["sandbox", "--action", "connect", "--id", "sb1", "--kind", "ssh", "--public-key-stdin", "--config", config.path])
    }

    func testConfigClampsAndDefaultsOff() {
        XCTAssertFalse(SandboxHostingConfig().enabled)
        XCTAssertEqual(SandboxHostingConfig(enabled: true, maxSandboxes: 99, placement: "x").clamped,
                       SandboxHostingConfig(enabled: true, maxSandboxes: 10, placement: "members"))
    }

    func testDecodesConnectorState() throws {
        let json = #"[{"id":"a1","state":"running","size":{"cpus":2},"hostname":"box","handle":{},"meshIp":"100.64.0.9","expiresAt":"2026-10-02T00:00:00.123456Z","updatedAt":"2026-10-01T00:00:00Z"}]"#
        let list = try JSONDecoder().decode([LocalSandbox].self, from: Data(json.utf8))
        XCTAssertTrue(list[0].isActive)
        XCTAssertNotNil(list[0].expiry)
    }

    @MainActor
    func testValidation() {
        XCTAssertTrue(ThrowawayHosting.validID("abc_1-2"))
        XCTAssertFalse(ThrowawayHosting.validID("../x"))
        XCTAssertFalse(ThrowawayHosting.validHost("a;b"))
        XCTAssertFalse(ThrowawayHosting.validKeyLine("ssh-ed25519 AAA'; rm -rf ~"))
        XCTAssertEqual(ThrowawayHosting.knownHostsLine(host: "100.64.0.9", port: 22, hostKey: "ssh-ed25519 AAAA comment"),
                       "100.64.0.9 ssh-ed25519 AAAA\n")
    }
}
