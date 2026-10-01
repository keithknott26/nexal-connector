import XCTest
@testable import nexal_vmhost

final class SpecDecodingTests: XCTestCase {
    var dir: String!

    override func setUpWithError() throws {
        dir = NSTemporaryDirectory() + "vmhost-test-\(UUID().uuidString.prefix(8))"
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        FileManager.default.createFile(atPath: dir + "/disk.raw", contents: Data([0]))
        FileManager.default.createFile(atPath: dir + "/seed.iso", contents: Data([0]))
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(atPath: dir)
    }

    func json(_ overrides: [String: Any] = [:]) -> Data {
        var d: [String: Any] = [
            "sandboxId": "sb1", "hostname": "box", "cpus": 2, "memoryMB": 2048,
            "diskPath": dir + "/disk.raw", "seedPath": dir + "/seed.iso",
            "consoleLog": dir + "/console.log", "controlSocket": dir + "/c.sock",
            "guestSocket": dir + "/g.sock", "desktop": false, "keepAwake": false,
        ]
        for (k, v) in overrides { d[k] = v }
        return try! JSONSerialization.data(withJSONObject: d)
    }

    func decodeValidate(_ o: [String: Any] = [:]) throws -> VMSpec {
        let s = try VMSpec.decode(from: json(o))
        try SpecValidator.validate(s)
        return s
    }

    func assertInvalid(_ o: [String: Any], _ needle: String, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertThrowsError(try decodeValidate(o), file: file, line: line) {
            XCTAssertTrue("\($0)".contains(needle), "got: \($0)", file: file, line: line)
        }
    }

    func testValidSpecWithDefaults() throws {
        let s = try decodeValidate()
        XCTAssertEqual(s.graceSeconds, 30)
        XCTAssertEqual(s.cpus, 2)
        XCTAssertFalse(s.wantsDesktop)
        XCTAssertNotNil(s.guest)
    }

    func testEmptySeedAndMissingGuestSocket() throws {
        var d = try JSONSerialization.jsonObject(with: json()) as! [String: Any]
        d["seedPath"] = ""
        d.removeValue(forKey: "guestSocket")
        let s = try VMSpec.decode(from: JSONSerialization.data(withJSONObject: d))
        try SpecValidator.validate(s)
        XCTAssertNil(s.seed)
        XCTAssertNil(s.guest)
    }

    func testMalformedJSON() {
        XCTAssertThrowsError(try VMSpec.decode(from: Data("{".utf8)))
        XCTAssertThrowsError(try VMSpec.decode(from: Data("{}".utf8)))
    }

    func testRelativePathRejected() { assertInvalid(["diskPath": "disk.raw"], "absolute") }
    func testDotDotRejected() { assertInvalid(["diskPath": dir + "/../x/disk.raw"], "..") }
    func testMissingDiskRejected() { assertInvalid(["diskPath": dir + "/nope.raw"], "does not exist") }
    func testBadResources() {
        assertInvalid(["cpus": 0], "cpus")
        assertInvalid(["memoryMB": 64], "memoryMB")
    }

    func testSymlinkDiskRejected() throws {
        try FileManager.default.createSymbolicLink(atPath: dir + "/link.raw", withDestinationPath: dir + "/disk.raw")
        assertInvalid(["diskPath": dir + "/link.raw"], "symlink")
    }

    func testSymlinkConsoleLogRejected() throws {
        try FileManager.default.createSymbolicLink(atPath: dir + "/log", withDestinationPath: "/etc/passwd")
        assertInvalid(["consoleLog": dir + "/log"], "symlink")
    }

    func testMissingParentRejected() {
        assertInvalid(["controlSocket": dir + "/missing/c.sock"], "parent")
    }

    func testSocketPathTooLong() {
        assertInvalid(["controlSocket": dir + "/" + String(repeating: "a", count: 120)], "too long")
    }

    func testSameSocketRejected() { assertInvalid(["guestSocket": dir + "/c.sock"], "differ") }

    func testMACAndGrace() throws {
        assertInvalid(["macAddress": "zz"], "macAddress")
        let s = try decodeValidate(["macAddress": "02:00:00:aa:bb:cc", "stopGraceSeconds": 5])
        XCTAssertEqual(s.graceSeconds, 5)
        assertInvalid(["stopGraceSeconds": 0], "stopGraceSeconds")
    }

    func testSharedDirs() throws {
        let sd = dir + "/share"
        try FileManager.default.createDirectory(atPath: sd, withIntermediateDirectories: true)
        _ = try decodeValidate(["sharedDirs": [["tag": "drive", "path": sd, "readOnly": true]]])
        assertInvalid(["sharedDirs": [["tag": "drive", "path": dir + "/disk.raw"]]], "not a directory")
        assertInvalid(["sharedDirs": [["tag": "a/b", "path": sd]]], "tag")
    }

    func testClampResources() {
        let r = clampResources(cpus: 64, memoryMB: 1_000_000, minCPU: 1, maxCPU: 8,
                               minMemory: 128 << 20, maxMemory: 16 << 30)
        XCTAssertEqual(r.cpus, 8)
        XCTAssertEqual(r.memoryBytes, 16 << 30)
        let lo = clampResources(cpus: 0, memoryMB: 1, minCPU: 1, maxCPU: 8, minMemory: 128 << 20, maxMemory: 16 << 30)
        XCTAssertEqual(lo.cpus, 1)
        XCTAssertEqual(lo.memoryBytes, 128 << 20)
    }
}
