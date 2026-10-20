import Foundation
import XCTest
@testable import NexalMac

final class SpeedSweepTests: XCTestCase {
    private func sweep(_ speeds: [Double?]) -> RemoteInferenceSweep {
        let samples = speeds.enumerated().map { i, s in
            RemoteInferenceSweep.Sample(call: i + 1, firstTokenMs: 200, tokensPerSec: s ?? 0, tokens: 128, totalMs: 4000, ok: s != nil, error: s == nil ? "boom" : nil)
        }
        return RemoteInferenceSweep(endpoint: "http://100.86.1.2:8001/v1", model: "m", samples: samples)
    }

    func testDecodesTheConnectorsSweep() throws {
        let json = #"{"endpoint":"http://100.86.1.2:8001/v1","model":"m","samples":[{"call":1,"firstTokenMs":210,"tokensPerSec":30.5,"tokens":128,"totalMs":4300,"ok":true},{"call":2,"firstTokenMs":0,"tokensPerSec":0,"tokens":0,"totalMs":0,"ok":false,"error":"HTTP 500"}]}"#
        let s = try JSONDecoder().decode(RemoteInferenceSweep.self, from: Data(json.utf8))
        XCTAssertEqual(s.samples.count, 2)
        XCTAssertTrue(s.samples[0].ok)
        XCTAssertEqual(s.samples[1].error, "HTTP 500")
    }

    func testSummaryUsesTheMedianAndCountsFailures() throws {
        let summary = try XCTUnwrap(SweepSummary(sweep([30, 10, nil, 20]), expected: 40))
        XCTAssertEqual(summary.median, 20)
        XCTAssertEqual(summary.fastest, 30)
        XCTAssertEqual(summary.slowest, 10)
        XCTAssertEqual(summary.failed, 1)
        XCTAssertEqual(try XCTUnwrap(summary.percentOfExpected), 50, accuracy: 0.001)
        XCTAssertTrue(summary.line.contains("1 of 4 calls failed"))
    }

    func testSummaryIsNilWhenEveryCallFailed() {
        XCTAssertNil(SweepSummary(sweep([nil, nil]), expected: 10))
    }

    func testChipBandwidthTable() {
        XCTAssertEqual(ExpectedSpeed.bandwidth(chip: "Apple M2 Pro"), 200)
        XCTAssertEqual(ExpectedSpeed.bandwidth(chip: "Apple M4"), 120)
        XCTAssertEqual(ExpectedSpeed.bandwidth(chip: "apple m1 ultra"), 800)
        XCTAssertNil(ExpectedSpeed.bandwidth(chip: "Intel Core i9"))
        XCTAssertNil(ExpectedSpeed.bandwidth(chip: nil))
    }

    func testCommandAndValidation() {
        let c = CLICommand.remoteInferenceSweep(endpoint: "http://100.86.1.2:8001/v1", calls: 20)
        XCTAssertEqual(c.timeLimit, 1800)
    }
}
