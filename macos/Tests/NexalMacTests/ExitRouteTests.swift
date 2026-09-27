import XCTest
@testable import NexalMac

final class ExitRouteTests: XCTestCase {
    let pair = "nx-exit-" + String(repeating: "a", count: 32)
    func listing(_ selected: Bool) -> String {
        "Available Networks:\n\n  - ID: \(pair)\n    Network: 0.0.0.0/0, ::/0\n    Status: \(selected ? "Selected" : "Not Selected")\n"
    }
    func testSelectionComesFromDefaultRouteStatusOnly() {
        let parsed = NetworkService.parseExitRoutes(listing(false) + "\n  - ID: nexal-exit-other\n    Network: 192.168.1.0/24\n    Status: Selected\n")
        XCTAssertEqual(parsed.available, [pair])
        XCTAssertTrue(parsed.selected.isEmpty)
        XCTAssertEqual(NetworkService.parseExitRoutes(listing(true)).selected, [pair])
    }
    func testCommandSuccessWithoutSelectionIsNotSuccess() {
        var calls = [[String]]()
        XCTAssertThrowsError(try NetworkService.selectExitRoute(pair, previous: "nexal-exit") { args in
            calls.append(args)
            return (0, args == ["networks", "ls"] ? self.listing(false) : "")
        })
        XCTAssertTrue(calls.contains(["networks", "deselect", pair]))
        XCTAssertEqual(calls.last, ["networks", "select", "--append", "nexal-exit"])
    }
    func testUncheckMustBeConfirmedDeselected() throws {
        var calls = [[String]]()
        try NetworkService.selectExitRoute(nil, previous: pair) { args in
            calls.append(args)
            return (0, args == ["networks", "ls"] ? self.listing(false) : "")
        }
        XCTAssertEqual(calls, [["networks", "deselect", pair], ["networks", "ls"]])
        XCTAssertThrowsError(try NetworkService.selectExitRoute(nil, previous: pair) { args in
            (0, args == ["networks", "ls"] ? self.listing(true) : "")
        })
    }
    func testInvalidRouteNeverExecutesHelper() {
        XCTAssertThrowsError(try NetworkService.selectExitRoute("--all", previous: nil) { _ in XCTFail("Must not execute"); return (0, "") })
    }
    func testTeardownCLIIsExplicit() {
        XCTAssertEqual(CLICommand.exitRoute(tunnelAddress: "100.64.1.2", enabled: false).arguments(config: URL(fileURLWithPath: "/tmp/config.json")), ["exit-route", "--tunnel", "100.64.1.2", "--disable", "--config", "/tmp/config.json"])
    }
}
