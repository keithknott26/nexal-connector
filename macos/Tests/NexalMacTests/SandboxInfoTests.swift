import XCTest
@testable import NexalMac

final class SandboxInfoTests: XCTestCase {
    private func network(_ json: String) throws -> NetworkSandbox {
        try JSONDecoder().decode(NetworkSandbox.self, from: Data(json.utf8))
    }

    private func rows(_ info: SandboxInfo) -> [String: String] {
        Dictionary(info.sections.flatMap(\.rows).map { ($0.label, $0.value) }, uniquingKeysWith: { a, _ in a })
    }

    func testRunningVMShowsAddressesLoginAndSystem() throws {
        let box = try network("""
        {"id":"a","hostname":"sbx-a","kind":"vm","lifecycle":"persistent","state":"running","meshIp":"100.86.1.2",
         "imageName":"Ubuntu 24.04","imageVersion":"20260101","desktop":true,"runnerName":"Mac mini","runnerOnline":true,
         "size":"medium","cpu":4,"memoryMb":8192,"diskGb":40,"reach":"network"}
        """)
        let local = LocalSandbox(id: "a", state: "running", hostname: "sbx-a", meshIp: "100.86.1.2", lanIp: "192.168.1.9",
                                 expiresAt: nil, kind: "vm", lifecycle: "persistent", paused: nil)
        let r = rows(SandboxInfoBuilder.make(network: box, local: local, title: "Web"))
        XCTAssertEqual(r["Private network IP"], "100.86.1.2")
        XCTAssertEqual(r["Home network IP"], "192.168.1.9")
        XCTAssertEqual(r["User name"], "nexal")
        XCTAssertEqual(r["SSH command"], "ssh nexal@100.86.1.2")
        XCTAssertEqual(r["Screen Sharing"], "One-time password, issued on Connect")
        XCTAssertEqual(r["Size"], "Medium: 4 CPU, 8 GB memory, 40 GB disk")
        XCTAssertEqual(r["Reach"], "Everyone on this network")
        XCTAssertEqual(r["Runs on"], "Mac mini")
        XCTAssertNil(r["Expires"])
    }

    func testNoDesktopAndAppURL() throws {
        let box = try network(#"{"id":"b","kind":"vm","lifecycle":"persistent","state":"running","meshIp":"100.86.9.9","desktop":false,"appProfile":"jellyfin"}"#)
        let r = rows(SandboxInfoBuilder.make(network: box, local: nil, title: "Media"))
        XCTAssertEqual(r["Screen Sharing"], "No desktop in this VM")
        XCTAssertEqual(r["Jellyfin"], "http://100.86.9.9:8096")
    }

    func testContainerHasNoScreenSharingAndFailureAndOfflineRunner() throws {
        let box = try network(#"{"id":"c","kind":"devcontainer","state":"failed","failure":"disk full","runnerName":"Studio","runnerOnline":false}"#)
        let info = SandboxInfoBuilder.make(network: box, local: nil, title: "Dev")
        let r = rows(info)
        XCTAssertNil(r["Screen Sharing"])
        XCTAssertEqual(r["Problem"], "disk full")
        XCTAssertEqual(r["Runs on"], "Studio (offline)")
        XCTAssertEqual(info.stateText, "Failed")
    }

    func testPausedLocalAndNeverShowsSecrets() {
        let local = LocalSandbox(id: "d", state: "running", hostname: "sbx-d", meshIp: nil, lanIp: nil,
                                 expiresAt: nil, kind: "vm", lifecycle: "persistent", paused: true)
        let info = SandboxInfoBuilder.make(network: nil, local: local, title: "Paused")
        XCTAssertEqual(info.stateText, "Paused (Mac asleep)")
        XCTAssertEqual(rows(info)["Address"], "Not assigned yet")
        XCTAssertEqual(rows(info)["Runs on"], "This Mac")
        XCTAssertFalse(info.sections.flatMap(\.rows).contains { $0.label.lowercased().contains("password") && $0.copyable })
    }
}

final class SandboxLogReaderTests: XCTestCase {
    func testCleanStripsColourCodesAndCarriageReturns() {
        XCTAssertEqual(SandboxLogReader.clean("a\r\n\u{1B}[31mred\u{1B}[0m\rb\u{0}c\n"), "a\nredbc\n")
    }

    func testTailKeepsOnlyTheEndAndDropsTheCutLine() throws {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent("console-\(UUID().uuidString).log")
        defer { try? FileManager.default.removeItem(at: url) }
        try "first line\nsecond line\nthird line\n".write(to: url, atomically: true, encoding: .utf8)
        XCTAssertEqual(SandboxLogReader.tail(of: url, maxBytes: 100), "first line\nsecond line\nthird line\n")
        XCTAssertEqual(SandboxLogReader.tail(of: url, maxBytes: 16), "third line\n")
    }

    func testMissingLogIsNil() {
        XCTAssertNil(SandboxLogReader.tail(of: URL(fileURLWithPath: "/nonexistent/console.log")))
    }

    func testLogButtonOnlyForVMsHostedHere() throws {
        let url = URL(fileURLWithPath: "/tmp/console.log")
        let vm = LocalSandbox(id: "a", state: "running", hostname: nil, meshIp: nil, lanIp: nil, expiresAt: nil, kind: "vm", lifecycle: "persistent", paused: nil)
        let box = LocalSandbox(id: "b", state: "running", hostname: nil, meshIp: nil, lanIp: nil, expiresAt: nil, kind: "devcontainer", lifecycle: nil, paused: nil)
        XCTAssertEqual(SandboxInfoBuilder.make(network: nil, local: vm, title: "t", logURL: url).logURL, url)
        XCTAssertNil(SandboxInfoBuilder.make(network: nil, local: box, title: "t", logURL: url).logURL)
        XCTAssertNil(SandboxInfoBuilder.make(network: nil, local: vm, title: "t").logURL)
    }
}

final class SandboxPeerMatchTests: XCTestCase {
    func testMatchesAPeerToItsVMByMeshAddress() throws {
        let list = try JSONDecoder().decode([NetworkSandbox].self, from: Data("""
        [{"id":"a","meshIp":"100.86.1.1","state":"running"},{"id":"b","meshIp":"100.86.1.2","state":"running"},
         {"id":"c","meshIp":"100.86.1.3","state":"deleted"}]
        """.utf8))
        XCTAssertEqual(SandboxPeerMatch.sandbox(forMeshIp: "100.86.1.2", in: list)?.id, "b")
        XCTAssertNil(SandboxPeerMatch.sandbox(forMeshIp: "100.86.9.9", in: list))
        XCTAssertNil(SandboxPeerMatch.sandbox(forMeshIp: "100.86.1.3", in: list))
        XCTAssertNil(SandboxPeerMatch.sandbox(forMeshIp: nil, in: list))
        XCTAssertNil(SandboxPeerMatch.sandbox(forMeshIp: " ", in: list))
    }
}
