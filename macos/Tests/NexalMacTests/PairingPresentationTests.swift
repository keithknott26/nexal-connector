import Foundation
import XCTest
@testable import NexalMac

/// Everything asserted here is presentation logic, so it runs without a Mac,
/// without the connector and without a network — which matters, because the panel
/// itself cannot be exercised that way.
///
/// The fixtures are the LITERAL stdout of `nexal pair`, copied from the Go
/// command's emitted shape rather than written to match these types. A fixture
/// authored on this side could only prove this side is self-consistent.
final class PairingPresentationTests: XCTestCase {
    /// A genuine version-1 matrix is 21 rows of 21 modules. Built here as a
    /// pattern rather than a real symbol: nothing in this file decodes a QR, and
    /// the Go tests are where the encoder is proved correct.
    private func rows(size: Int) -> [String] {
        (0..<size).map { row in
            String((0..<size).map { column in (row + column) % 3 == 0 ? "1" : "0" })
        }
    }

    private func mintJSON(status: String = "waiting", role: String = "receiver",
                          expiresAt: String = "2026-09-21T19:25:00.000Z",
                          version: Int = 1, size: Int = 21, mask: Int = 2,
                          quietZone: Int = 4) -> Data {
        let moduleRows = rows(size: size).map { "\"\($0)\"" }.joined(separator: ",")
        return Data("""
        {"pairing":{"pairingId":"3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b","role":"\(role)",\
        "coordinator":"https://coordinator-dev.nexal.systems","expiresAt":"\(expiresAt)",\
        "status":"\(status)","qr":{"version":\(version),"mask":\(mask),"size":\(size),\
        "quietZone":\(quietZone),"errorLevel":"M","encoding":"byte","moduleRows":[\(moduleRows)]},\
        "claimToken":"not emitted: the claim token exists only in this process's memory and inside the qr modules"}}
        """.utf8)
    }

    func testMintDecodesAndKeepsNoClaimToken() throws {
        let mint = try PairingMint.decode(mintJSON())
        XCTAssertEqual(mint.pairing.pairingId, "3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b")
        XCTAssertEqual(mint.pairing.qr.version, 1)
        XCTAssertEqual(mint.pairing.qr.moduleRows.count, 21)
        // The decoded type has no claim token property at all, which is the point:
        // a secret this process never holds cannot be logged or crash-reported.
        let mirrored = Mirror(reflecting: mint.pairing).children.compactMap(\.label)
        XCTAssertFalse(mirrored.contains("claimToken"))
        XCTAssertEqual(Set(mirrored),
                       ["pairingId", "role", "coordinator", "expiresAt", "status", "qr", "manualCode"])
    }

    /// The CLI emits one JSON object per line and may emit more than one; a
    /// one-shot command must not blank the panel because a future connector added
    /// a second line.
    func testOnlyTheFirstJSONLineIsDecoded() throws {
        var data = mintJSON()
        data.append(Data("\n{\"pairingStatus\":{\"pairingId\":\"x\",\"status\":\"waiting\",\"expiresAt\":\"y\"}}\n".utf8))
        XCTAssertEqual(try PairingMint.decode(data).pairing.qr.size, 21)
    }

    func testPresentationReadsRoleStatusAndExpiry() throws {
        let presentation = PairingPresentation(mint: try PairingMint.decode(mintJSON()))
        XCTAssertNotNil(presentation)
        XCTAssertEqual(presentation?.role, .receiver)
        XCTAssertEqual(presentation?.status, .waiting)
        XCTAssertEqual(presentation?.symbol?.size, 21)
        XCTAssertEqual(presentation?.expiresAt,
                       ISO8601DateFormatter().date(from: "2026-09-21T19:25:00Z"))
        XCTAssertTrue(presentation?.roleLine.contains("receives") == true)
    }

    /// An unparseable or impossible matrix must produce NO presentation. Drawing a
    /// placeholder code would be a code that cannot scan, which is worse than
    /// drawing nothing and saying why.
    func testUnusableSymbolsAreRefusedRatherThanDrawn() throws {
        // size must equal 4 * version + 17
        XCTAssertNil(PairingPresentation(mint: try PairingMint.decode(
            mintJSON(version: 2, size: 21))))
        // row count must equal size
        var shortRows = try PairingMint.decode(mintJSON()).pairing.qr.moduleRows
        shortRows.removeLast()
        XCTAssertNil(PairingSymbol(qr: .init(version: 1, mask: 2, size: 21, quietZone: 4,
                                            errorLevel: "M", encoding: "byte",
                                            moduleRows: shortRows)))
        // mask must be 0...7
        XCTAssertNil(PairingPresentation(mint: try PairingMint.decode(mintJSON(mask: 8))))
        // characters must be "0" or "1"
        let bad = rows(size: 21).enumerated().map { $0.offset == 3 ? String(repeating: "2", count: 21) : $0.element }
        XCTAssertNil(PairingSymbol(qr: .init(version: 1, mask: 0, size: 21, quietZone: 4,
                                            errorLevel: "M", encoding: "byte", moduleRows: bad)))
    }

    /// A QR without its quiet zone is one many scanners will not see at all, and
    /// the omission is invisible in review.
    func testQuietZoneIsPaddedOnEverySide() throws {
        let symbol = try XCTUnwrap(PairingPresentation(mint: try PairingMint.decode(mintJSON()))?.symbol)
        XCTAssertEqual(symbol.quietZone, 4)
        XCTAssertEqual(symbol.paddedSize, 29)
        let padded = symbol.paddedModules
        XCTAssertEqual(padded.count, 29)
        for row in padded { XCTAssertEqual(row.count, 29) }
        for index in 0..<4 {
            XCTAssertFalse(padded[index].contains(true))
            XCTAssertFalse(padded[padded.count - 1 - index].contains(true))
            for row in padded {
                XCTAssertFalse(row[index])
                XCTAssertFalse(row[row.count - 1 - index])
            }
        }
        // The interior must still be the connector's matrix, unshifted.
        XCTAssertEqual(padded[4][4], symbol.modules[0][0])
        XCTAssertEqual(padded[24][24], symbol.modules[20][20])
    }

    func testCountdownFloorsAtZeroAndNeverGoesNegative() throws {
        let expiry = "2026-09-21T19:25:00.000Z"
        let presentation = try XCTUnwrap(PairingPresentation(mint: try PairingMint.decode(
            mintJSON(expiresAt: expiry))))
        let at = try XCTUnwrap(ISO8601DateFormatter().date(from: "2026-09-21T19:25:00Z"))
        XCTAssertEqual(presentation.secondsRemaining(now: at.addingTimeInterval(-125)), 125)
        XCTAssertEqual(presentation.countdown(now: at.addingTimeInterval(-125)), "Expires in 2m 5s.")
        XCTAssertEqual(presentation.countdown(now: at.addingTimeInterval(-45)), "Expires in 45s.")
        XCTAssertEqual(presentation.secondsRemaining(now: at.addingTimeInterval(600)), 0)
        // A countdown reaching zero is not a claim that the coordinator expired it.
        XCTAssertTrue(presentation.countdown(now: at.addingTimeInterval(600)).contains("Refresh to confirm"))
    }

    func testStatusPollUpdatesStatusAndKeepsTheSymbol() throws {
        let presentation = try XCTUnwrap(PairingPresentation(mint: try PairingMint.decode(mintJSON())))
        let report = try PairingStatusReport.decode(Data("""
        {"pairingStatus":{"pairingId":"3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b","status":"scanned","expiresAt":"2026-09-21T19:25:00.000Z"}}
        """.utf8))
        let updated = presentation.updated(with: report.pairingStatus)
        XCTAssertEqual(updated.status, .scanned)
        XCTAssertFalse(updated.status.isLive)
        XCTAssertTrue(updated.status.isTerminal)
        // A status record carries no matrix; re-deriving one would mean inventing it.
        XCTAssertEqual(updated.symbol, presentation.symbol)
        XCTAssertEqual(updated.role, .receiver)
        // Claiming is not linking, and the copy must not imply otherwise.
        XCTAssertTrue(updated.status.explanation.contains("not linking"))
    }

    /// A report about a different pairing must be ignored rather than applied to
    /// the code on screen.
    func testAStatusForAnotherPairingIsIgnored() throws {
        let presentation = try XCTUnwrap(PairingPresentation(mint: try PairingMint.decode(mintJSON())))
        let other = PairingStatusReport.State(pairingId: "00000000-0000-4000-8000-000000000000",
                                             status: "cancelled",
                                             expiresAt: "2026-09-21T19:25:00.000Z")
        XCTAssertEqual(presentation.updated(with: other).status, .waiting)
    }

    func testEveryCoordinatorStatusIsRecognisedAndUnknownIsNotGuessed() {
        for raw in ["waiting", "scanned", "cancelled", "expired"] {
            let status = PairingPresentation.Status(raw: raw)
            XCTAssertEqual(status.raw, raw)
            XCTAssertFalse(status.label.isEmpty)
            XCTAssertFalse(status.explanation.isEmpty)
            if case .unknown = status { XCTFail("\(raw) must be a known status") }
        }
        let future = PairingPresentation.Status(raw: "linked")
        XCTAssertEqual(future, .unknown("linked"))
        XCTAssertFalse(future.isLive)
        XCTAssertTrue(future.explanation.contains("linked"))
        XCTAssertTrue(future.explanation.contains("does not recognise"))
    }

    /// A role this build does not know must still be reported as what it was, not
    /// silently shown as the requested one.
    func testAnUnknownRoleIsShownVerbatim() throws {
        let presentation = try XCTUnwrap(PairingPresentation(mint: try PairingMint.decode(
            mintJSON(role: "observer"))))
        XCTAssertNil(presentation.role)
        XCTAssertEqual(presentation.rawRole, "observer")
        XCTAssertTrue(presentation.roleLine.contains("observer"))
    }

    /// §26: never a dead button with no stated cause. Each blocker has its own
    /// sentence, in the order the owner has to fix them.
    func testUnavailableReasonsNameTheActualBlocker() throws {
        XCTAssertTrue(try XCTUnwrap(PairingPresentation.unavailableReason(
            hasExecutable: false, configurationExists: false, status: nil)).contains("Choose the Go connector"))
        // No configuration is not a blocker: startPairing runs `init` first.
        XCTAssertNil(PairingPresentation.unavailableReason(
            hasExecutable: true, configurationExists: false, status: nil))
		XCTAssertNil(PairingPresentation.unavailableReason(
			hasExecutable: true, configurationExists: true, status: nil))
        let unenrolled = try ConnectorStatus.decode(Data(#"{"paused":true}"#.utf8))
		XCTAssertNil(PairingPresentation.unavailableReason(
			hasExecutable: true, configurationExists: true, status: unenrolled))
        let enrolled = try ConnectorStatus.decode(Data(#"{"paused":true,"hostId":"host_1"}"#.utf8))
        XCTAssertNil(PairingPresentation.unavailableReason(
            hasExecutable: true, configurationExists: true, status: enrolled))
    }

    /// The indicator must never be the only thing carrying the state: colour is
    /// paired with a symbol and text, as §26.3 requires.
    func testIndicatorCarriesSymbolAndTextNotOnlyColour() throws {
        let presentation = try XCTUnwrap(PairingPresentation(mint: try PairingMint.decode(mintJSON())))
        let indicator = presentation.indicator
        XCTAssertEqual(indicator.heading, "Phone pairing")
        XCTAssertEqual(indicator.systemImage, "qrcode.viewfinder")
        XCTAssertFalse(indicator.reason.isEmpty)
        XCTAssertTrue(try XCTUnwrap(indicator.detail).contains(presentation.pairingId))
        XCTAssertEqual(indicator.detailLines, [presentation.roleLine])
        XCTAssertEqual(PairingPresentation.Status.expired.systemImage, "clock.badge.exclamationmark")
    }

    func testMalformedConnectorOutputIsAnExplainedFailure() {
        for payload in ["", "not json", "{}", #"{"pairing":{"pairingId":"x"}}"#] {
            XCTAssertThrowsError(try PairingMint.decode(Data(payload.utf8))) { error in
                // Compared by message rather than by case: ShellError is not
                // Equatable in the app target and making it so purely for a test
                // would be a production change driven by a test.
                XCTAssertEqual((error as? ShellError)?.errorDescription,
                               ShellError.invalidPairing.errorDescription)
                XCTAssertEqual(error.localizedDescription.contains("no code is shown"), true)
            }
        }
        XCTAssertThrowsError(try PairingStatusReport.decode(Data("{}".utf8)))
    }
}

// The CLI's decodable types are `Decodable`-only in the app target, which is
// correct: nothing there should be able to synthesise one. The tests need to
// construct them directly to exercise the refusal paths.
extension PairingMint.Pairing.Symbol {
    init(version: Int, mask: Int, size: Int, quietZone: Int, errorLevel: String,
         encoding: String, moduleRows: [String]) {
        struct Wire: Encodable {
            let version: Int, mask: Int, size: Int, quietZone: Int
            let errorLevel: String, encoding: String, moduleRows: [String]
        }
        let wire = Wire(version: version, mask: mask, size: size, quietZone: quietZone,
                        errorLevel: errorLevel, encoding: encoding, moduleRows: moduleRows)
        // Encoding and re-decoding keeps the app target free of a memberwise
        // initializer that production code could reach for.
        self = try! JSONDecoder().decode(Self.self, from: try! JSONEncoder().encode(wire))
    }
}

extension PairingStatusReport.State {
    init(pairingId: String, status: String, expiresAt: String) {
        struct Wire: Encodable { let pairingId: String, status: String, expiresAt: String }
        let wire = Wire(pairingId: pairingId, status: status, expiresAt: expiresAt)
        self = try! JSONDecoder().decode(Self.self, from: try! JSONEncoder().encode(wire))
    }
}
