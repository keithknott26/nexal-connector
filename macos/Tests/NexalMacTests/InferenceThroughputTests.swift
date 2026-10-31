import XCTest
@testable import NexalMac

final class InferenceThroughputTests: XCTestCase {
    private let json = """
    {"updated":1800000100,"since":1800000000,"bucketSeconds":5,"offloadName":"RunPod GPU",
     "points":[{"t":1800000090,"localTps":10,"offloadTps":0,"localRequests":1,"offloadRequests":0},
               {"t":1800000095,"localTps":0,"offloadTps":20,"localRequests":0,"offloadRequests":1}],
     "totals":{"localRequests":1,"localTokens":50,"offloadRequests":1,"offloadTokens":100}}
    """

    private func decode() throws -> InferenceThroughput {
        try JSONDecoder().decode(InferenceThroughput.self, from: Data(json.utf8))
    }

    func testDecodesWhatTheConnectorWrites() throws {
        let s = try decode()
        XCTAssertEqual(s.points.count, 2)
        XCTAssertEqual(s.totals.offloadTokens, 100)
        XCTAssertEqual(s.offloadLabel, "RunPod GPU")
        XCTAssertTrue(s.hasActivity)
    }

    func testDecodesPromptSizesAndTheOffloadLimit() throws {
        let withLimit = json.replacingOccurrences(
            of: "\"totals\":{\"localRequests\":1,\"localTokens\":50,\"offloadRequests\":1,\"offloadTokens\":100}",
            with: "\"totals\":{\"localRequests\":1,\"localTokens\":50,\"offloadRequests\":1,\"offloadTokens\":100},"
                + "\"maxPromptTokens\":2000,"
                + "\"requests\":[{\"t\":1800000090,\"promptTokens\":300,\"offloaded\":false},"
                + "{\"t\":1800000095,\"promptTokens\":5000,\"offloaded\":true}]")
        let s = try JSONDecoder().decode(InferenceThroughput.self, from: Data(withLimit.utf8))
        XCTAssertEqual(s.maxPromptTokens, 2000)
        XCTAssertEqual(s.requests?.count, 2)
        XCTAssertEqual(s.requests?.last?.offloaded, true)
        // an older connector's file has neither field
        let old = try decode()
        XCTAssertNil(old.maxPromptTokens)
        XCTAssertNil(old.requests)
    }

    func testTwoLinesOneForThisMacAndOneForTheOffload() throws {
        let samples = try decode().samples()
        XCTAssertEqual(samples.count, 4)
        XCTAssertEqual(Set(samples.map(\.series)), ["This Mac", "RunPod GPU"])
        XCTAssertEqual(samples.first { $0.series == "RunPod GPU" && $0.tokensPerSecond > 0 }?.tokensPerSecond, 20)
    }

    func testUnnamedOffloadIsStillLabelled() throws {
        let s = try JSONDecoder().decode(InferenceThroughput.self, from: Data(json.replacingOccurrences(of: "RunPod GPU", with: "").utf8))
        XCTAssertEqual(s.offloadLabel, "Offloaded")
    }

    func testStaleWhenTheConnectorStoppedWriting() throws {
        let s = try decode()
        XCTAssertFalse(s.isStale(now: Date(timeIntervalSince1970: 1_800_000_110)))
        XCTAssertTrue(s.isStale(now: Date(timeIntervalSince1970: 1_800_000_200)))
    }

    func testRecentRateAveragesTheLastSteps() throws {
        let s = try decode()
        XCTAssertEqual(s.recentRate(offloaded: false, seconds: 10), 5, accuracy: 0.001)
        XCTAssertEqual(s.recentRate(offloaded: true, seconds: 10), 10, accuracy: 0.001)
    }

    func testReadsTheFileBesidesTheConfiguration() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let config = dir.appendingPathComponent("config.json")
        XCTAssertNil(InferenceThroughput.read(besides: config), "no file yet")
        try Data(json.utf8).write(to: dir.appendingPathComponent(InferenceThroughput.fileName))
        XCTAssertEqual(InferenceThroughput.read(besides: config)?.totals.localTokens, 50)
        try Data("not json".utf8).write(to: dir.appendingPathComponent(InferenceThroughput.fileName))
        XCTAssertNil(InferenceThroughput.read(besides: config), "a broken file is ignored")
    }

    func testRemoteURLAcceptsOnlyMeshAddresses() {
        XCTAssertEqual(InferenceThroughput.remoteURL("http://100.64.0.5:8001/v1")?.absoluteString, "http://100.64.0.5:8001/nexal/throughput")
        XCTAssertEqual(InferenceThroughput.remoteURL("100.64.0.5:8001")?.absoluteString, "http://100.64.0.5:8001/nexal/throughput")
        XCTAssertNil(InferenceThroughput.remoteURL("http://8.8.8.8:8001/v1"))
        XCTAssertNil(InferenceThroughput.remoteURL("http://100.64.0.5/v1"))
        XCTAssertNil(InferenceThroughput.remoteURL(""))
    }
}
