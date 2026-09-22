import Foundation
import XCTest
@testable import NexalMac

/// The window shows exactly one stage, so these tests are about which one wins when
/// several signals could each claim the screen. Pure values, no connector, no network
/// -- which is the point: the view this drives cannot be tested, so the decision was
/// moved out of it to somewhere that can be.
final class ConnectorStageTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_758_480_000)

    /// Same fixture shape as PairingPresentationTests: the literal stdout of
    /// `nexal pair`, not a record authored to match these types.
    private func rows(size: Int) -> [String] {
        (0..<size).map { row in
            String((0..<size).map { column in (row + column) % 3 == 0 ? "1" : "0" })
        }
    }

    private func pairing(status: String, expiresAt: Date) throws -> PairingPresentation {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let moduleRows = rows(size: 21).map { "\"\($0)\"" }.joined(separator: ",")
        let json = Data("""
        {"pairing":{"pairingId":"3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b","role":"receiver",\
        "coordinator":"https://coordinator-dev.nexal.systems",\
        "expiresAt":"\(formatter.string(from: expiresAt))",\
        "status":"\(status)","qr":{"version":1,"mask":2,"size":21,\
        "quietZone":4,"errorLevel":"M","encoding":"byte","moduleRows":[\(moduleRows)]}}}
        """.utf8)
        return try XCTUnwrap(PairingPresentation(mint: try PairingMint.decode(json)))
    }

    private func derive(hasAnswered: Bool = true, isRunning: Bool = true,
                        isEnrolled: Bool = true,
                        pairing: PairingPresentation? = nil) -> ConnectorStage {
        ConnectorStage.derive(hasAnswered: hasAnswered, isRunning: isRunning,
                              isEnrolled: isEnrolled, pairing: pairing, now: now)
    }

    /// A slow first poll must not flash "not running" and then correct itself. An
    /// alarming wrong state is worse than a moment of honest silence.
    func testNoAnswerYetIsNotReportedAsOffline() {
        XCTAssertEqual(derive(hasAnswered: false, isRunning: false), .starting)
        XCTAssertEqual(derive(hasAnswered: false, isRunning: true), .starting)
    }

    /// A pairing record outlives the process that minted it. Showing that code after
    /// the connector stopped invites someone to scan something nothing is listening
    /// for, so "not running" must beat "here is a code".
    func testAStoppedConnectorNeverKeepsShowingACode() throws {
        let live = try pairing(status: "waiting", expiresAt: now.addingTimeInterval(240))
        XCTAssertEqual(derive(isRunning: false, pairing: live), .offline)
        XCTAssertEqual(derive(isRunning: true, pairing: live), .showingCode)
    }

    /// Reported success outranks a local clock. Losing a completed pairing because the
    /// record also looks expired would send the owner round the loop for nothing.
    func testScannedWinsOverAnExpiryThatHasAlreadyPassed() throws {
        let scanned = try pairing(status: "scanned", expiresAt: now.addingTimeInterval(-1))
        XCTAssertEqual(derive(pairing: scanned), .paired)
        XCTAssertTrue(derive(pairing: scanned).isOnNetwork)
    }

    /// A code the coordinator would refuse must not stay on screen looking usable.
    func testALocallyExpiredCodeStopsBeingDisplayed() throws {
        let expired = try pairing(status: "waiting", expiresAt: now.addingTimeInterval(-1))
        XCTAssertEqual(derive(pairing: expired), .pairingEnded(.expired))
        XCTAssertFalse(derive(pairing: expired).isShowingCode)
        // Exactly at the boundary the code is already refused, not still live.
        let boundary = try pairing(status: "waiting", expiresAt: now)
        XCTAssertEqual(derive(pairing: boundary), .pairingEnded(.expired))
    }

    func testTerminalStatesFromTheServerAreCarriedThroughRatherThanFlattened() throws {
        let cancelled = try pairing(status: "cancelled", expiresAt: now.addingTimeInterval(240))
        XCTAssertEqual(derive(pairing: cancelled), .pairingEnded(.cancelled))
        // An unfamiliar state from a newer connector is reported as itself, never
        // mapped onto one this build happens to understand.
        let unknown = try pairing(status: "reticulating", expiresAt: now.addingTimeInterval(240))
        XCTAssertEqual(derive(pairing: unknown), .pairingEnded(.unknown("reticulating")))
        XCTAssertEqual(derive(pairing: unknown).guidance?.contains("reticulating"), true)
    }

    func testRunningAndEnrolledWithNoCodeAsksForOne() {
        XCTAssertEqual(derive(isEnrolled: true, pairing: nil), .needsPairing)
    }

    /// "Not running" is false for a connector that is running but has never joined an
    /// account, and a false cause is what makes someone restart a process that was
    /// never the problem. The two must stay distinguishable.
    func testRunningButUnjoinedIsNotReportedAsNotRunning() {
        XCTAssertEqual(derive(isRunning: true, isEnrolled: false, pairing: nil), .notJoined)
        XCTAssertEqual(derive(isRunning: false, isEnrolled: false, pairing: nil), .offline)
        XCTAssertNotEqual(ConnectorStage.notJoined.headline, ConnectorStage.offline.headline)
        XCTAssertFalse(ConnectorStage.notJoined.isOnNetwork)
    }

    /// Graphs of a network this Mac has not joined are decoration, and the code is on
    /// screen in exactly one stage.
    func testOnlyPairedCountsAsOnTheNetworkAndOnlyOneStageShowsTheCode() throws {
        let live = try pairing(status: "waiting", expiresAt: now.addingTimeInterval(240))
        let stages: [ConnectorStage] = [
            .starting, .offline, .notJoined, .needsPairing,
            derive(pairing: live), .paired, .pairingEnded(.expired),
        ]
        XCTAssertEqual(stages.filter(\.isOnNetwork), [.paired])
        XCTAssertEqual(stages.filter(\.isShowingCode), [.showingCode])
    }

    /// Every stage says something, and the two that are pure waiting say nothing
    /// rather than inventing filler.
    func testEveryStageHasAHeadlineAndOnlyWaitingStatesOmitGuidance() {
        let stages: [ConnectorStage] = [
            .starting, .offline, .notJoined, .needsPairing, .showingCode, .paired,
            .pairingEnded(.expired), .pairingEnded(.cancelled), .pairingEnded(.unknown("x")),
        ]
        for stage in stages {
            XCTAssertFalse(stage.headline.isEmpty, "\(stage) has no headline")
        }
        XCTAssertNil(ConnectorStage.starting.guidance)
        XCTAssertNil(ConnectorStage.paired.guidance)
        for stage in stages where stage != .starting && stage != .paired {
            XCTAssertNotNil(stage.guidance, "\(stage) leaves the person with nothing to do")
        }
    }
}
