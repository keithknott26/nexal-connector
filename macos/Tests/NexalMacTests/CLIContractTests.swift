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

    func testResourcePolicyAndTelemetryExtrasAreOptional() throws {
        let sparse = try ConnectorStatus.decode(Data(#"{"paused":true}"#.utf8))
        XCTAssertNil(sparse.resourcePolicy)
        XCTAssertNil(sparse.coordinatorHealthy)
        XCTAssertNil(sparse.lastOutcome)
        let full = try ConnectorStatus.decode(Data(#"{"paused":false,"coordinatorHealthy":true,"lastOutcome":"completed","resourcePolicy":{"memoryLimitBytes":268435456,"reserveMemoryBytes":4294967296,"idleSeconds":300},"telemetry":{"known":true,"synthetic":false,"ownerActive":true,"availableMemoryBytes":1024,"idleSeconds":12,"totalMemoryBytes":2048}}"#.utf8))
        XCTAssertEqual(full.resourcePolicy?.memoryLimitBytes, 268_435_456)
        XCTAssertEqual(full.resourcePolicy?.reserveMemoryBytes, 4_294_967_296)
        XCTAssertEqual(full.telemetry?.idleSeconds, 12)
        XCTAssertEqual(full.telemetry?.totalMemoryBytes, 2_048)
        XCTAssertEqual(full.coordinatorHealthy, true)
        XCTAssertEqual(full.lastOutcome, "completed")
    }

    // A connector that reports only part of the policy must not make the whole
    // status undecodable, which would blank the panel rather than degrade it.
    func testPartialResourcePolicyDoesNotFailTheWholeStatus() throws {
        let partial = try ConnectorStatus.decode(Data(#"{"paused":true,"resourcePolicy":{"idleSeconds":300}}"#.utf8))
        XCTAssertNil(partial.resourcePolicy?.memoryLimitBytes)
        XCTAssertEqual(partial.resourcePolicy?.idleSeconds, 300)
        XCTAssertTrue(partial.paused)
    }

    func testStatusRejectsMissingPauseState() {
        XCTAssertThrowsError(try ConnectorStatus.decode(Data(#"{"version":"0.1.0"}"#.utf8)))
        XCTAssertTrue(try ConnectorStatus.decode(Data(#"{"paused":true,"future":42}"#.utf8)).paused)
    }

    func testRejectsPATHAndArbitraryExecutable() {
        XCTAssertThrowsError(try ExecutableSelection.approve(URL(fileURLWithPath: "/usr/bin/true")))
        XCTAssertThrowsError(try ExecutableSelection.approve(URL(fileURLWithPath: "/tmp/nexal")))
    }

    /// The owner asked for the deployed coordinator to be the default and for the
    /// app never to look on the LAN. Both halves are asserted here: the prefilled
    /// origin is the deployed https one, and the loopback preview is opt-in rather
    /// than the state a fresh install starts in.
    @MainActor
    func testDefaultCoordinatorIsTheDeployedHTTPSOriginAndPreviewIsOptIn() {
        let model = AppModel()
        XCTAssertEqual(model.coordinator, "https://coordinator-dev.nexal.systems")
        XCTAssertTrue(model.coordinator.hasPrefix("https://"))
        XCTAssertFalse(model.localPreview)
        // No default may point at the machine itself or at a private range.
        for host in ["127.0.0.1", "localhost", "0.0.0.0", "192.168.", "10.", "169.254."] {
            XCTAssertFalse(model.coordinator.contains(host), "default must not be local: \(host)")
        }
    }
}
