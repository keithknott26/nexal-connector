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
        let band = ExpectedEstimate(low: 40, high: 60, chips: "Apple M4 Pro")
        let summary = try XCTUnwrap(SweepSummary(sweep([30, 10, nil, 20]), expected: band))
        XCTAssertEqual(summary.median, 20)
        XCTAssertEqual(summary.fastest, 30)
        XCTAssertEqual(summary.slowest, 10)
        XCTAssertEqual(summary.failed, 1)
        XCTAssertEqual(summary.verdict, .below)
        XCTAssertTrue(summary.line.contains("1 of 4 calls failed"))
    }

    func testVerdictAllowsSlackAroundTheBand() throws {
        let band = ExpectedEstimate(low: 40, high: 60, chips: "x")
        XCTAssertEqual(try XCTUnwrap(SweepSummary(sweep([50, 50]), expected: band)).verdict, .inLine)
        XCTAssertEqual(try XCTUnwrap(SweepSummary(sweep([36, 36]), expected: band)).verdict, .inLine)
        XCTAssertEqual(try XCTUnwrap(SweepSummary(sweep([80, 80]), expected: band)).verdict, .above)
        XCTAssertEqual(try XCTUnwrap(SweepSummary(sweep([50, 50]), expected: nil)).verdict, .unknown)
    }

    func testEstimateIsARangeFromWeightsAndChip() throws {
        let e = try XCTUnwrap(ExpectedSpeed.estimate(weights: 2.3e9, shares: [1], chips: ["Apple M4 Pro"], hopMs: 1))
        XCTAssertEqual(e.low, 273e9 * 0.6 / 2.3e9, accuracy: 0.01)
        XCTAssertEqual(e.high, 273e9 * 0.85 / 2.3e9, accuracy: 0.01)
        XCTAssertEqual(e.chips, "Apple M4 Pro")
    }

    func testBinnedChipsWidenTheRange() throws {
        let fixed = try XCTUnwrap(ExpectedSpeed.estimate(weights: 2e9, shares: [1], chips: ["Apple M4 Pro"], hopMs: 1))
        let binned = try XCTUnwrap(ExpectedSpeed.estimate(weights: 2e9, shares: [1], chips: ["Apple M4 Max"], hopMs: 1))
        XCTAssertGreaterThan(binned.high / binned.low, fixed.high / fixed.low)
    }

    func testAPoolAddsTimesAndHopsSoItIsSlowerThanOneMac() throws {
        let solo = try XCTUnwrap(ExpectedSpeed.estimate(weights: 4e9, shares: [1], chips: ["Apple M2"], hopMs: 2))
        let pool = try XCTUnwrap(ExpectedSpeed.estimate(weights: 4e9, shares: [0.5, 0.5], chips: ["Apple M2", "Apple M2"], hopMs: 2))
        XCTAssertLessThan(pool.high, solo.high)
    }

    func testUnknownChipGivesNoEstimate() {
        XCTAssertNil(ExpectedSpeed.estimate(weights: 2e9, shares: [1], chips: ["Apple M9 Ultra"], hopMs: 1))
        XCTAssertNil(ExpectedSpeed.estimate(weights: 2e9, shares: [0.5, 0.5], chips: ["Apple M2", nil], hopMs: 1))
        XCTAssertNil(ExpectedSpeed.estimate(weights: 0, shares: [1], chips: ["Apple M2"], hopMs: 1))
    }

    func testSummaryIsNilWhenEveryCallFailed() {
        XCTAssertNil(SweepSummary(sweep([nil, nil]), expected: 10))
    }

    func testChipBandwidthTable() {
        XCTAssertEqual(ExpectedSpeed.bandwidth(chip: "Apple M2 Pro")?.high, 200)
        XCTAssertEqual(ExpectedSpeed.bandwidth(chip: "Apple M4")?.low, 120)
        XCTAssertEqual(ExpectedSpeed.bandwidth(chip: "apple m1 ultra")?.high, 800)
        XCTAssertEqual(ExpectedSpeed.bandwidth(chip: "Apple M5 Pro")?.high, 307)
        XCTAssertEqual(ExpectedSpeed.bandwidth(chip: "Apple M5 Max")?.low, 460)
        XCTAssertEqual(ExpectedSpeed.bandwidth(chip: "Apple M5 Max")?.high, 614)
        XCTAssertEqual(ExpectedSpeed.bandwidth(chip: "Apple M5 Ultra")?.high, 1200)
        for chip in ["m1", "m1 pro", "m1 max", "m1 ultra", "m2", "m2 pro", "m2 max", "m2 ultra", "m3", "m3 pro", "m3 max", "m3 ultra", "m4", "m4 pro", "m4 max", "m5", "m6"] {
            let bw = ExpectedSpeed.bandwidth(chip: "Apple " + chip.uppercased())
            XCTAssertNotNil(bw, chip)
            XCTAssertLessThanOrEqual(bw?.low ?? 1, bw?.high ?? 0, chip)
        }
        XCTAssertNil(ExpectedSpeed.bandwidth(chip: "Intel Core i9"))
        XCTAssertNil(ExpectedSpeed.bandwidth(chip: nil))
    }

    func testCommandAndValidation() {
        let c = CLICommand.remoteInferenceSweep(endpoint: "http://100.86.1.2:8001/v1", calls: 20)
        XCTAssertEqual(c.timeLimit, 1800)
    }
}
