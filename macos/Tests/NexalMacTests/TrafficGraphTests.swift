import XCTest
@testable import NexalMac

final class TrafficGraphTests: XCTestCase {
    func testAxisTopIsARoundNumberAboveThePeak() {
        XCTAssertEqual(TrafficGraphSpec.niceMax(0), 5)      // idle: a small fixed scale, not "1, 1, 0, 0"
        XCTAssertEqual(TrafficGraphSpec.niceMax(1.6), 5)
        XCTAssertEqual(TrafficGraphSpec.niceMax(37), 50)
        XCTAssertEqual(TrafficGraphSpec.niceMax(180), 200)
        XCTAssertEqual(TrafficGraphSpec.niceMax(4_600), 10_000)
    }

    func testAnIdleLineNeverDipsBelowTheBaseline() {
        let pts = [0.0, 5, 10, 15, 20, 25].enumerated().map { i, t in
            TrafficGraphSpec.Point(t: t, v: i == 1 ? 2 : 0)
        }
        let svg = TrafficGraphSpec(received: pts, sent: pts, receivedTotal: "1 KB", sentTotal: "1 KB").svg
        // Baseline y = top + plotH = 24 + 62 = 86; no control point may go below it.
        let numbers = svg.split(whereSeparator: { !"0123456789.".contains($0) }).compactMap { Double($0) }
        XCTAssertFalse(svg.contains("NaN"))
        XCTAssertTrue(numbers.allSatisfy { $0 <= 340 })
    }
}
