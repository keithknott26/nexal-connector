import Foundation
import XCTest
@testable import NexalMac

final class ScannerSettingsTests: XCTestCase {
    private let config = URL(fileURLWithPath: "/tmp/scanner config.json")
    func testFolderAndEngineArgumentsStaySeparateWithoutShellExpansion() {
        let roots = ["/Users/test/My Scripts", "/tmp/$(touch unsafe);folder"]
        let arguments = CLICommand.securityConfigure(roots: roots, engine: "/Applications/neXal.app/Contents/MacOS/yr", enabled: true).arguments(config: config)
        XCTAssertEqual(arguments, ["security", "configure", "--enabled=true", "--root", roots[0], "--root", roots[1], "--engine", "/Applications/neXal.app/Contents/MacOS/yr", "--config", config.path])
        XCTAssertFalse(arguments.contains("/bin/sh"))
    }
    func testPausePreservesSavedRootsAndEngine() {
        XCTAssertEqual(CLICommand.securityConfigure(roots: [], engine: nil, enabled: false).arguments(config: config), ["security", "configure", "--enabled=false", "--config", config.path])
    }
    func testStatusDoesNotScanOrApproveAndApprovalIsExplicit() {
        XCTAssertEqual(CLICommand.securityStatus.arguments(config: config), ["security", "status", "--config", config.path])
        XCTAssertEqual(CLICommand.securityBaseline.arguments(config: config), ["security", "baseline", "--approve", "--config", config.path])
        XCTAssertEqual(CLICommand.securityScan.timeLimit, 130)
        XCTAssertEqual(CLICommand.securityBaseline.timeLimit, 130)
        XCTAssertEqual(CLICommand.securityStatus.timeLimit, 20)
    }
    func testLocalFindingsContractRetainsPathsOnlyInLocalView() throws {
        XCTAssertEqual(CLICommand.securityFindings.arguments(config: config), ["security", "findings", "--config", config.path])
        let reply = try JSONDecoder().decode(LocalScannerFindingsReply.self, from: Data(#"{"findings":[{"eventId":"test","path":"/Users/test/My Scripts/check.py","ruleId":"nexal_python_reverse_shell","engine":"yara_x","contentSha256":"abc","observedAt":"2026-09-26T12:00:00Z","severity":"medium","testOnly":false}]}"#.utf8))
        XCTAssertEqual(reply.findings.first?.path, "/Users/test/My Scripts/check.py")
        XCTAssertTrue(ScannerPresentation.rule("nexal_python_reverse_shell").contains("does not prove"))
        XCTAssertTrue(ScannerPresentation.rule("unknown").contains("context"))
    }
    func testExpiredFindingCountIsOptionalForOlderConnectors() throws {
        let old = try JSONDecoder().decode(ScannerReply.self, from: Data(#"{"enabled":false,"status":"disabled"}"#.utf8))
        XCTAssertNil(old.expiredEvents)
        let current = try JSONDecoder().decode(ScannerReply.self, from: Data(#"{"enabled":true,"status":"idle","expiredEvents":3}"#.utf8))
        XCTAssertEqual(current.expiredEvents, 3)
    }
    func testZeroCoverageAndMissingEngineDecodeHonestly() throws {
        let reply = try JSONDecoder().decode(ScannerReply.self, from: Data(#"{"enabled":true,"status":"engine_unavailable","roots":[],"filesScanned":0,"coverage":"configured_roots"}"#.utf8))
        XCTAssertEqual(reply.filesScanned, 0)
        XCTAssertNil(reply.findings)
        XCTAssertEqual(ScannerPresentation.status(reply.status), "Scan engine unavailable")
        XCTAssertEqual(ScannerPresentation.status(nil), "Not reported")
        XCTAssertTrue(ScannerPresentation.error("scan_limit").contains("skipped"))
    }
    func testPeerScannerMetadataDecodeDoesNotRequireNewFields() throws {
        let metadata = try JSONDecoder().decode(ConnectorStatus.HostDetails.self, from: Data(#"{"threatScannerStatus":"idle","threatRulesVersion":"v1","threatScanCoverage":"configured_roots","threatScanFilesScanned":0,"lastThreatScanAt":"2026-09-26T12:00:00Z"}"#.utf8))
        XCTAssertEqual(metadata.threatScanFilesScanned, 0)
        XCTAssertEqual(metadata.threatRulesVersion, "v1")
        XCTAssertNil(metadata.threatScanFindings)
        XCTAssertNotEqual(ScannerPresentation.date(metadata.lastThreatScanAt), "Not reported")
    }
}
