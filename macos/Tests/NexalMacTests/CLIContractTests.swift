import Foundation
import XCTest
@testable import NexalMac

final class CLIContractTests: XCTestCase {
    let config = URL(fileURLWithPath: "/Users/test/Library/Application Support/Nexal/config.json")

    func testArgumentsRemainSeparate() {
        let name = "Mac; $(touch /tmp/not-executed)"
        let args = CLICommand.initialize(coordinator: "https://example.test", name: name,
                                         memoryMiB: 256, reserveMiB: 4096).arguments(config: config)
        XCTAssertEqual(args[4], name)
        XCTAssertEqual(args.suffix(2), ["--config", config.path])
        XCTAssertFalse(args.contains("/bin/sh"))
    }

    func testCodeNeverAppearsInArguments() {
        XCTAssertEqual(CLICommand.enroll.arguments(config: config),
                       ["enroll", "--code-stdin", "--config", config.path])
    }

    func testLocalPreviewIsExplicitAndLoopbackOnly() {
        let args = CLICommand.initializeLocalPreview(name: "M4",
                  memoryMiB: 256, reserveMiB: 8192).arguments(config: config)
        XCTAssertTrue(args.contains("http://127.0.0.1:8787"))
        XCTAssertTrue(args.contains("--dev-loopback"))
        XCTAssertTrue(args.contains("--dev-secrets"))
        XCTAssertFalse(args.contains("--dev-assume-idle"))
        XCTAssertFalse(args.contains("--dev-private-pull"))
        let production = CLICommand.initialize(coordinator: "https://example.test",
                  name: "M4", memoryMiB: 256, reserveMiB: 8192).arguments(config: config)
        XCTAssertFalse(production.contains("--dev-loopback"))
        XCTAssertFalse(production.contains("--dev-secrets"))
    }

    func testPreviewAndProductionConfigurationsAreSeparate() {
        XCTAssertNotEqual(ConnectorProcess.configURL, ConnectorProcess.localPreviewConfigURL)
        XCTAssertTrue(ConnectorProcess.localPreviewConfigURL.path.contains("Nexal-Local-Preview"))
        XCTAssertFalse(ConnectorProcess.configURL.path.contains("KWK"))
    }

    func testCommandsMatchGoContract() {
        for (command, name) in [(CLICommand.run, "run"), (.status, "status"),
                                (.pause, "pause"), (.resume, "resume"), (.acceptJobs, "accept-jobs")] {
            XCTAssertEqual(command.arguments(config: config).first, name)
        }
    }

    func testAcceptJobsUsesAuthenticatedCLIAndDoesNotFakeTelemetry() {
        let args = CLICommand.acceptJobs.arguments(config: config)
        XCTAssertEqual(args, ["accept-jobs", "--config", config.path])
        XCTAssertFalse(args.contains("--dev-assume-idle"))
    }

    func testManualAcceptanceStatusIsOptionalForOldConnectors() throws {
        let old = try ConnectorStatus.decode(Data(#"{"paused":true}"#.utf8))
        XCTAssertNil(old.manualAcceptanceSupported)
        let updated = try ConnectorStatus.decode(Data(#"{"paused":false,"manualAcceptanceSupported":true,"ownerActivityOverride":true,"acceptJobsUntil":"2026-09-19T13:00:00.000Z","executionBlocker":"insufficient approved memory headroom"}"#.utf8))
        XCTAssertEqual(updated.ownerActivityOverride, true)
        XCTAssertEqual(updated.executionBlocker, "insufficient approved memory headroom")
    }

    func testStatusRejectsMissingPauseState() {
        XCTAssertThrowsError(try ConnectorStatus.decode(Data(#"{"version":"0.1.0"}"#.utf8)))
        XCTAssertTrue(try ConnectorStatus.decode(Data(#"{"paused":true,"future":42}"#.utf8)).paused)
    }

    func testRejectsPATHAndArbitraryExecutable() {
        XCTAssertThrowsError(try ExecutableSelection.approve(URL(fileURLWithPath: "/usr/bin/true")))
        XCTAssertThrowsError(try ExecutableSelection.approve(URL(fileURLWithPath: "/tmp/nexal")))
    }
}
