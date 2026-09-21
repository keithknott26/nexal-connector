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

    // MARK: - Phone pairing

    /// The three pairing invocations, asserted argument for argument against
    /// connector/CLI-CONTRACT.md. `--no-poll` is the important one: without it the
    /// CLI blocks until the pairing resolves, which would sit inside the app's
    /// 20-second process bound and time out on every pairing nobody scans instantly.
    func testPairingCommandsMatchGoContract() {
        XCTAssertEqual(CLICommand.pair(role: .receiver).arguments(config: config),
                       ["pair", "--role", "receiver", "--no-poll", "--config", config.path])
        XCTAssertEqual(CLICommand.pair(role: .donor).arguments(config: config),
                       ["pair", "--role", "donor", "--no-poll", "--config", config.path])
        let id = "3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b"
        XCTAssertEqual(CLICommand.pairingStatus(pairingId: id).arguments(config: config),
                       ["pair", "--status", id, "--config", config.path])
        XCTAssertEqual(CLICommand.cancelPairing(pairingId: id).arguments(config: config),
                       ["pair", "--cancel", id, "--config", config.path])
    }

    /// A pairing id arrives from the connector and goes back out as an argument.
    /// It must stay a single separate argument: no shell is involved anywhere in
    /// ConnectorProcess, and this asserts that the app is not the place where that
    /// stops being true.
    func testPairingIdentifierStaysOneArgument() {
        let hostile = "id; touch /tmp/not-executed --role donor"
        let args = CLICommand.cancelPairing(pairingId: hostile).arguments(config: config)
        XCTAssertEqual(args, ["pair", "--cancel", hostile, "--config", config.path])
        XCTAssertFalse(args.contains("/bin/sh"))
        XCTAssertEqual(args.filter { $0 == "--role" }.count, 0)
    }

    /// The role is an enum precisely so an unchecked string cannot reach the CLI.
    func testOnlyTheTwoContractRolesExist() {
        XCTAssertEqual(PairingRole.allCases.map(\.rawValue), ["receiver", "donor"])
        XCTAssertNil(PairingRole(rawValue: "observer"))
        for role in PairingRole.allCases {
            XCTAssertFalse(role.label.isEmpty)
            XCTAssertFalse(role.explanation.isEmpty)
        }
    }

    /// The claim token authorizes a phone to claim this Mac. The CLI puts a
    /// disclaimer in that field rather than the secret, and this app must not
    /// decode, store or display it either way.
    func testPairingOutputCarriesNoClaimToken() throws {
        let json = #"""
        {"pairing":{"pairingId":"3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b","role":"receiver","coordinator":"https://coordinator-dev.nexal.systems","expiresAt":"2026-09-21T19:25:00.000Z","status":"waiting","qr":{"version":1,"mask":2,"size":21,"quietZone":4,"errorLevel":"M","encoding":"byte","moduleRows":["111111101010101111111","100000101010101000001","101110101010101011101","101110100000001011101","101110101111101011101","100000101010101000001","111111101010101111111","000000001010100000000","101010101010101010101","010101010101010101010","101010101010101010101","010101010101010101010","101010101010101010101","000000001010101010101","111111101010101010101","100000101010101010101","101110101010101010101","101110101010101010101","101110101010101010101","100000101010101010101","111111101010101010101"]},"claimToken":"not emitted"}}
        """#
        let mint = try PairingMint.decode(Data(json.utf8))
        XCTAssertEqual(mint.pairing.qr.moduleRows.count, 21)
        XCTAssertFalse(Mirror(reflecting: mint.pairing).children.contains { $0.label == "claimToken" })
    }

    /// The app is not allowed to have its own QR encoder: two encoders can drift,
    /// and only a phone would ever notice. The matrix must come from the CLI.
    @MainActor
    func testTheAppHasNoPairingStateUntilTheConnectorMintsOne() {
        let model = AppModel()
        XCTAssertNil(model.pairing)
        XCTAssertNil(model.pairingProblem)
        XCTAssertEqual(model.pairingRole, .receiver)
        // Without a chosen connector there is nothing to run, and the reason must
        // be stated rather than leaving a dead button.
        XCTAssertNotNil(model.pairingUnavailableReason)
    }
}
