import Foundation
import XCTest
@testable import NexalMac

final class HoneypotSettingsTests: XCTestCase {
    private let config = URL(fileURLWithPath: "/tmp/honeypot config.json")

    func testHoneypotArgumentsFollowCanaryConvention() {
        for action in ["status", "enable", "disable"] {
            XCTAssertEqual(CLICommand.honeypot(action: action).arguments(config: config),
                           ["honeypot", "--action", action, "--config", config.path])
        }
        XCTAssertEqual(CLICommand.honeypot(action: "status").timeLimit, 20)
    }

    func testDecodesFullReply() throws {
        let json = #"{"enabled":true,"status":"degraded","ports":[{"port":2222,"service":"ssh","listening":true},{"port":3389,"service":"rdp","listening":false,"error":"in_use"}],"triggers":2,"lastTriggeredAt":"2026-09-29T12:00:00Z","pendingEvents":1,"recent":[{"observedAt":"2026-09-29T12:00:00Z","service":"ssh","port":2222,"sourceAddress":"100.64.0.7","sourceClass":"mesh"}],"description":"fake services"}"#
        let reply = try JSONDecoder().decode(HoneypotReply.self, from: Data(json.utf8))
        XCTAssertEqual(reply.ports?.count, 2)
        XCTAssertEqual(reply.ports?[1].error, "in_use")
        XCTAssertEqual(reply.recent?.first?.sourceAddress, "100.64.0.7")
        XCTAssertEqual(HoneypotPresentation.status(reply), "Some decoy services could not start")
        XCTAssertEqual(HoneypotPresentation.sourceClass("mesh"), "neXal network")
        XCTAssertNotEqual(HoneypotPresentation.date(reply.lastTriggeredAt), "Never")
    }

    func testDecodesMinimalDisabledReply() throws {
        let reply = try JSONDecoder().decode(HoneypotReply.self, from: Data(#"{"enabled":false,"status":"disabled"}"#.utf8))
        XCTAssertNil(reply.ports)
        XCTAssertEqual(HoneypotPresentation.status(reply), "Off on this Mac")
        XCTAssertEqual(HoneypotPresentation.date(nil), "Never")
    }
}
