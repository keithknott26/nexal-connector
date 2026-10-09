import XCTest
@testable import NexalMac
final class NetworkSetupCheckTests: XCTestCase {
    func testGlobalState() {
        XCTAssertEqual(NetworkSetupCheck.parseGlobalState("Firewall is enabled. (State = 1)"), .enabled)
        XCTAssertEqual(NetworkSetupCheck.parseGlobalState("Firewall is disabled. (State = 0)"), .disabled)
        XCTAssertEqual(NetworkSetupCheck.parseGlobalState(""), .unknown)
    }

    func testBootpdRule() {
        let allowed = "Total number of apps = 1\n\n1 :  /usr/libexec/bootpd\n\t ( Allow incoming connections )\n"
        let blocked = "Total number of apps = 1\n\n1 :  /usr/libexec/bootpd\n\t ( Block incoming connections )\n"
        let other = "Total number of apps = 1\n\n1 :  /Applications/Foo.app\n\t ( Allow incoming connections )\n"
        XCTAssertEqual(NetworkSetupCheck.parseBootpdRule(allowed), .allowed)
        XCTAssertEqual(NetworkSetupCheck.parseBootpdRule(blocked), .blocked)
        XCTAssertEqual(NetworkSetupCheck.parseBootpdRule(other), .notListed)
    }

    func testLittleSnitchDetection() {
        let ps = "/sbin/launchd\n/Library/SystemExtensions/X/at.obdev.littlesnitch.networkextension.systemextension/Contents/MacOS/at.obdev.littlesnitch.networkextension\n"
        XCTAssertTrue(NetworkSetupCheck.parseLittleSnitchRunning(ps))
        XCTAssertFalse(NetworkSetupCheck.parseLittleSnitchRunning("/sbin/launchd\n/usr/libexec/bootpd\n"))
    }

    func testEvaluateMessages() {
        let on = NetworkSetupCheck.evaluate(globalState: "Firewall is enabled. (State = 1)", listApps: "", processList: "")
        XCTAssertTrue(on.firewallBlocksBootpd)
        XCTAssertTrue(on.messages[0].contains("/usr/libexec/bootpd"))
        let ok = NetworkSetupCheck.evaluate(globalState: "Firewall is disabled. (State = 0)", listApps: "", processList: "")
        XCTAssertFalse(ok.hasProblem)
        let ls = NetworkSetupCheck.evaluate(globalState: "Firewall is disabled. (State = 0)", listApps: "", processList: "littlesnitch")
        XCTAssertTrue(ls.hasProblem)
    }
}
