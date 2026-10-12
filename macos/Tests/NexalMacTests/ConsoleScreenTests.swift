import XCTest
@testable import NexalMac

final class ConsoleScreenTests: XCTestCase {
    func testShowsTheLastRowsAndScrollsTheRestOff() {
        let text = (1...40).map { "line \($0)" }.joined(separator: "\n") + "\n"
        let screen = ConsoleScreen.lines(from: text)
        XCTAssertEqual(screen.count, 24)
        XCTAssertEqual(screen.first, "line 17")
        XCTAssertEqual(screen.last, "line 40")
    }

    func testWrapsLongLinesAtEightyColumns() {
        let screen = ConsoleScreen.lines(from: String(repeating: "x", count: 170) + "\n")
        XCTAssertEqual(screen.map(\.count), [80, 80, 10])
    }

    func testLoginPromptWithoutNewlineStaysOnTheLastRow() {
        let screen = ConsoleScreen.lines(from: "Debian GNU/Linux 13 sbx hvc0\n\nsbx login: ")
        XCTAssertEqual(screen, ["Debian GNU/Linux 13 sbx hvc0", "", "sbx login: "])
    }

    func testEmptyLogIsAnEmptyScreen() {
        XCTAssertEqual(ConsoleScreen.lines(from: ""), [])
    }
}
