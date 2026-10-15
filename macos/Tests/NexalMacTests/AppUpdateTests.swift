import XCTest
@testable import NexalMac

final class AppUpdateTests: XCTestCase {
    func testVersionComparison() {
        XCTAssertTrue(AppUpdate.isNewer("0.4.2", than: "0.4.1"))
        XCTAssertTrue(AppUpdate.isNewer("v0.10.0", than: "0.9.9"))
        XCTAssertTrue(AppUpdate.isNewer("1.0", than: "0.9.9"))
        XCTAssertFalse(AppUpdate.isNewer("0.4.1", than: "0.4.1"))
        XCTAssertFalse(AppUpdate.isNewer("0.4.1.0", than: "0.4.1"))
        XCTAssertFalse(AppUpdate.isNewer("0.4.0", than: "0.4.1"))
        XCTAssertFalse(AppUpdate.isNewer("garbage", than: "0.4.1"))
    }

    private func payload(tag: String = "v0.5.0", draft: Bool = false, prerelease: Bool = false, assets: [String] = [
        "Nexal-Connector-0.5.0-universal.dmg", "Nexal-Connector-0.5.0-universal.dmg.sha256"]) -> Data {
        let list = assets.map { #"{"name":"\#($0)","browser_download_url":"https://github.com/x/y/releases/download/\#(tag)/\#($0)"}"# }.joined(separator: ",")
        return Data(#"{"tag_name":"\#(tag)","draft":\#(draft),"prerelease":\#(prerelease),"body":"notes","html_url":"https://github.com/x/y/releases/tag/\#(tag)","assets":[\#(list)]}"#.utf8)
    }

    func testReleaseParsingPicksSignedDMGAndChecksum() throws {
        let release = try XCTUnwrap(AppUpdate.release(from: payload()))
        XCTAssertEqual(release.version, "0.5.0")
        XCTAssertEqual(release.dmgName, "Nexal-Connector-0.5.0-universal.dmg")
        XCTAssertTrue(release.checksumURL?.absoluteString.hasSuffix(".dmg.sha256") == true)
    }

    func testUnsignedDraftAndPrereleaseAreNeverOffered() {
        XCTAssertNil(AppUpdate.release(from: payload(assets: ["Nexal-Connector-0.5.0-universal-UNSIGNED-DO-NOT-DISTRIBUTE.dmg"])))
        XCTAssertNil(AppUpdate.release(from: payload(draft: true)))
        XCTAssertNil(AppUpdate.release(from: payload(prerelease: true)))
        XCTAssertNil(AppUpdate.release(from: payload(assets: [])))
        XCTAssertNil(AppUpdate.release(from: Data("not json".utf8)))
    }

    func testChecksumLineParsing() {
        let hex = String(repeating: "ab", count: 32)
        XCTAssertEqual(AppUpdate.expectedDigest(in: "\(hex)  a.dmg\n", for: "a.dmg"), hex)
        XCTAssertEqual(AppUpdate.expectedDigest(in: hex, for: "a.dmg"), hex)
        XCTAssertNil(AppUpdate.expectedDigest(in: "\(hex)  other.dmg", for: "a.dmg"))
        XCTAssertNil(AppUpdate.expectedDigest(in: "short  a.dmg", for: "a.dmg"))
    }

    func testSHA256OfFile() throws {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try Data("abc".utf8).write(to: url)
        defer { try? FileManager.default.removeItem(at: url) }
        XCTAssertEqual(try AppUpdate.sha256(of: url), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
    }

    func testInstallScriptIsValidShellAndKeepsRollback() throws {
        let script = AppUpdate.installScript(pid: 42, staged: "/tmp/it's/new.app", destination: "/Applications/neXal-Connector.app", stagingDir: "/tmp/it's")
        XCTAssertTrue(script.contains("kill -0 42"))
        XCTAssertTrue(script.contains(".old"))
        XCTAssertTrue(script.contains("with administrator privileges"))
        XCTAssertTrue(script.contains("/usr/bin/open"))
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("\(UUID().uuidString).sh")
        try script.write(to: file, atomically: true, encoding: .utf8)
        defer { try? FileManager.default.removeItem(at: file) }
        let check = Process()
        check.executableURL = URL(fileURLWithPath: "/bin/bash"); check.arguments = ["-n", file.path]
        try check.run(); check.waitUntilExit()
        XCTAssertEqual(check.terminationStatus, 0, "the generated installer must parse as shell")
    }
}
