import Foundation
import XCTest
@testable import NexalMac

final class InferenceInstallTests: XCTestCase {
    private func event(_ json: String) throws -> InferenceInstallEvent {
        try InferenceInstallEvent.decode(line: Data(json.utf8))
    }

    // MARK: Events

    func testDecodesEveryEventType() throws {
        XCTAssertEqual(try event(#"{"event":"start","modelId":"m","totalBytes":1000}"#),
                       .start(modelId: "m", totalBytes: 1000))
        XCTAssertEqual(
            try event(#"{"event":"progress","file":"a.safetensors","bytes":10,"total":40,"overallBytes":10,"overallTotal":1000}"#),
            .progress(file: "a.safetensors", bytes: 10, total: 40, overallBytes: 10, overallTotal: 1000))
        XCTAssertEqual(try event(#"{"event":"verified","file":"a.safetensors"}"#), .verified(file: "a.safetensors"))
        XCTAssertEqual(
            try event(#"{"event":"done","modelId":"m","dir":"/d","manifestSha256":"ab","state":"installed_unmeasured","howToUse":["l1","l2"]}"#),
            .done(InferenceInstallSummary(modelId: "m", dir: "/d", manifestSha256: "ab",
                                          state: .installedUnmeasured, howToUse: ["l1", "l2"])))
        XCTAssertEqual(
            try event(#"{"event":"error","code":"disk_full","message":"No space","fix":"Free 10 GB"}"#),
            .error(code: "disk_full", message: "No space", fix: "Free 10 GB"))
        XCTAssertEqual(try event(#"{"event":"error","code":"x","message":"m"}"#),
                       .error(code: "x", message: "m", fix: nil))
    }

    func testUnknownEventIsIgnoredNotFatalAndGarbageIsRejected() throws {
        XCTAssertEqual(try event(#"{"event":"heartbeat"}"#), .other("heartbeat"))
        XCTAssertThrowsError(try event("not json")) { XCTAssertEqual($0 as? InferenceInstallError, .unreadable) }
        XCTAssertThrowsError(try event(#"{"event":"start","modelId":"m"}"#)) {
            XCTAssertEqual($0 as? InferenceInstallError, .unreadable)
        }
        XCTAssertThrowsError(try event(#"{"event":"progress","file":"f"}"#))
    }

    // MARK: Progress math

    func testProgressFractionAndLabels() {
        var p = InstallProgress(modelId: "m")
        let t0 = Date(timeIntervalSince1970: 1000)
        p.apply(.start(modelId: "m", totalBytes: 2_147_483_648), at: t0)
        XCTAssertEqual(p.fraction, 0)
        p.apply(.progress(file: "a", bytes: 0, total: 0, overallBytes: 536_870_912, overallTotal: 2_147_483_648), at: t0)
        XCTAssertEqual(p.fraction, 0.25, accuracy: 1e-9)
        XCTAssertEqual(p.percentLabel, "25%")
        XCTAssertEqual(p.sizeLabel, "0.5 GB of 2.0 GB")
        XCTAssertNil(p.speedLabel)
    }

    func testSpeedIsMeasuredAndSmoothed() {
        var p = InstallProgress(modelId: "m")
        let t0 = Date(timeIntervalSince1970: 1000)
        p.apply(.progress(file: "a", bytes: 0, total: 1, overallBytes: 0, overallTotal: 100_000_000), at: t0)
        // 10 MiB in 1 s.
        p.apply(.progress(file: "a", bytes: 0, total: 1, overallBytes: 10_485_760, overallTotal: 100_000_000),
                at: t0.addingTimeInterval(1))
        XCTAssertEqual(p.speedLabel, "10.0 MB/s")
        // Samples closer than the interval are ignored.
        p.apply(.progress(file: "a", bytes: 0, total: 1, overallBytes: 11_000_000, overallTotal: 100_000_000),
                at: t0.addingTimeInterval(1.1))
        XCTAssertEqual(p.speedLabel, "10.0 MB/s")
        // 20 MiB/s next second: 0.7 * 10 + 0.3 * ~20.4 ≈ 13.
        p.apply(.progress(file: "a", bytes: 0, total: 1, overallBytes: 10_485_760 + 20_971_520, overallTotal: 100_000_000),
                at: t0.addingTimeInterval(2))
        let speed = p.bytesPerSecond ?? 0
        XCTAssertGreaterThan(speed, 10_485_760)
        XCTAssertLessThan(speed, 20_971_520)
    }

    func testOverallNeverMovesBackwardsAndFractionIsClamped() {
        var p = InstallProgress(modelId: "m")
        p.apply(.progress(file: "a", bytes: 5, total: 5, overallBytes: 800, overallTotal: 1000))
        p.apply(.progress(file: "b", bytes: 1, total: 9, overallBytes: 100, overallTotal: 1000))
        XCTAssertEqual(p.overallBytes, 800)
        p.apply(.progress(file: "b", bytes: 9, total: 9, overallBytes: 5000, overallTotal: 1000))
        XCTAssertEqual(p.fraction, 1)
        XCTAssertEqual(InstallProgress(modelId: "m").fraction, 0) // unknown total is 0, not NaN
    }

    func testVerifiedFilesAreCountedOnce() {
        var p = InstallProgress(modelId: "m")
        p.apply(.verified(file: "a")); p.apply(.verified(file: "a")); p.apply(.verified(file: "b"))
        XCTAssertEqual(p.verifiedFiles, ["a", "b"])
    }

    // MARK: State labels

    func testStateLabelsAreHonest() throws {
        XCTAssertEqual(InferenceModelState.installedUnmeasured.label,
                       "Installed and verified; not runnable until runtime release gates are completed")
        XCTAssertTrue(InferenceModelState.damaged.label.hasPrefix("Damaged"))
        XCTAssertEqual(InferenceModelState.other("ready").label, "State reported by the connector: ready")
        XCTAssertEqual(InferenceModelState.installedUnmeasured.tone, .caution)
        XCTAssertEqual(InferenceModelState.damaged.tone, .bad)
        // An unknown state, even one called "ready", is never shown as good.
        XCTAssertEqual(InferenceModelState.other("ready").tone, .neutral)
        XCTAssertFalse(InferenceModelState.installedUnmeasured.label.lowercased().contains("running"))
    }

    // MARK: Status / verify / remove

    private let statusJSON = #"""
    {"schemaVersion":1,"inferenceDir":"/i","models":[{"modelId":"qwen3-4b-q4","state":"installed_unmeasured",
     "revision":"0123456789abcdef0123","hfRepo":"o/r","license":"apache-2.0","bytesOnDisk":2147483648,
     "installedAt":"2026-10-09T12:00:00Z","dir":"/d","manifestSha256":"ab","estimatedMemory":true,"problems":[]},
     {"modelId":"bad","displayName":"Bad One","state":"damaged","revision":"r","hfRepo":"o/r","license":"mit",
     "bytesOnDisk":1,"installedAt":"t","dir":"/d2","manifestSha256":"cd","estimatedMemory":true,"problems":["a.bin missing"]}],
     "incomplete":[{"modelId":"part","dir":"/p","bytesOnDisk":1073741824}]}
    """#

    func testDecodesInstalledModelsAndPartials() throws {
        let r = try InferenceInstalledReport.decode(Data(statusJSON.utf8))
        XCTAssertEqual(r.models.map(\.modelId), ["qwen3-4b-q4", "bad"])
        XCTAssertEqual(r.models[0].title, "qwen3-4b-q4") // displayName is optional
        XCTAssertEqual(r.models[1].title, "Bad One")
        XCTAssertEqual(r.models[0].state, .installedUnmeasured)
        XCTAssertEqual(r.models[1].state, .damaged)
        let lines = InferenceInstallText.row(r.models[0])
        XCTAssertEqual(lines[0], "2.0 GB on disk · revision 0123456789ab · installed 2026-10-09T12:00:00Z")
        XCTAssertTrue(lines[1].contains("estimate"))
        XCTAssertTrue(InferenceInstallText.row(r.models[1]).contains("a.bin missing"))
        XCTAssertEqual(r.incomplete.map(\.modelId), ["part"])
        XCTAssertEqual(InferenceInstallText.partialLine(r.incomplete[0]),
                       "Partial download · 1.0 GB on disk · not installed")
    }

    func testStatusWithoutIncompleteKeyStillDecodes() throws {
        let r = try InferenceInstalledReport.decode(Data(#"{"schemaVersion":1,"models":[]}"#.utf8))
        XCTAssertTrue(r.models.isEmpty); XCTAssertTrue(r.incomplete.isEmpty)
    }

    func testStatusRejectsUnknownSchemaAndGarbage() {
        let v2 = statusJSON.replacingOccurrences(of: "\"schemaVersion\":1", with: "\"schemaVersion\":2")
        XCTAssertThrowsError(try InferenceInstalledReport.decode(Data(v2.utf8))) {
            XCTAssertEqual($0 as? InferenceInstallError, .unsupportedSchema(2))
        }
        XCTAssertThrowsError(try InferenceInstalledReport.decode(Data("nope".utf8))) {
            XCTAssertEqual($0 as? InferenceInstallError, .unreadable)
        }
        XCTAssertThrowsError(try InferenceInstalledReport.decode(Data(#"{"schemaVersion":1}"#.utf8)))
        XCTAssertNotNil(InferenceInstallError.unsupportedSchema(2).errorDescription)
    }

    func testUnknownInstalledStateStillDecodes() throws {
        let json = statusJSON.replacingOccurrences(of: "installed_unmeasured", with: "quantum")
        XCTAssertEqual(try InferenceInstalledReport.decode(Data(json.utf8)).models[0].state, .other("quantum"))
    }

    func testVerifyResultOkAndFailedDocument() throws {
        let ok = try InferenceVerifyResult.decode(Data(
            #"{"schemaVersion":1,"modelId":"m","ok":true,"full":true,"state":"installed_unmeasured","revision":"r","files":[{"name":"a","status":"ok"}],"problems":[]}"#.utf8))
        XCTAssertTrue(ok.ok)
        XCTAssertTrue(ok.message.hasPrefix("Verified"))
        // Exit 1 with ok=false still prints a document: it is a result, not a crash.
        let bad = try InferenceVerifyResult.decode(Data(
            #"{"schemaVersion":1,"modelId":"m","ok":false,"full":true,"state":"damaged","revision":"r","files":[{"name":"a.bin","status":"hash_mismatch"}],"problems":[]}"#.utf8))
        XCTAssertFalse(bad.ok)
        XCTAssertEqual(bad.state, .damaged)
        XCTAssertTrue(bad.message.contains("a.bin: hash mismatch"))
        XCTAssertThrowsError(try InferenceVerifyResult.decode(Data(#"{"schemaVersion":2,"modelId":"m","ok":true}"#.utf8))) {
            XCTAssertEqual($0 as? InferenceInstallError, .unsupportedSchema(2))
        }
        XCTAssertThrowsError(try InferenceVerifyResult.decode(Data("{}".utf8)))
    }

    // MARK: Model ids and commands

    func testModelIDValidation() {
        XCTAssertTrue(InferenceModelID.isValid("qwen3-14b-q4"))
        XCTAssertTrue(InferenceModelID.isValid("llama3.1_8b"))
        XCTAssertFalse(InferenceModelID.isValid(""))
        XCTAssertFalse(InferenceModelID.isValid("--yes"))
        XCTAssertFalse(InferenceModelID.isValid("a b"))
        XCTAssertFalse(InferenceModelID.isValid("../x"))
        XCTAssertFalse(InferenceModelID.isValid("é"))
    }

    func testCommandArguments() {
        let config = URL(fileURLWithPath: "/Users/test/Library/Application Support/Nexal/config.json")
        XCTAssertEqual(CLICommand.inferenceInstall(modelId: "m").arguments(config: config),
                       ["inference", "install", "m", "--json", "--config", config.path])
        XCTAssertEqual(CLICommand.inferenceStatus.arguments(config: config),
                       ["inference", "status", "--json", "--config", config.path])
        XCTAssertEqual(CLICommand.inferenceVerify(modelId: "m").arguments(config: config),
                       ["inference", "verify", "m", "--json", "--config", config.path])
        XCTAssertEqual(CLICommand.inferenceRemove(modelId: "m").arguments(config: config),
                       ["inference", "remove", "m", "--yes", "--json", "--config", config.path])
        XCTAssertGreaterThan(CLICommand.inferenceInstall(modelId: "m").timeLimit, 3600)
        XCTAssertGreaterThanOrEqual(CLICommand.inferenceVerify(modelId: "m").timeLimit, 120)
    }

    // MARK: Install offer

    private func offer(_ edit: (inout [String: Any]) -> Void) throws -> InferenceInstallOffer {
        var object = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(InferenceReportTests.fixture.utf8)) as? [String: Any])
        var models = object["models"] as? [[String: Any]] ?? []
        let i = try XCTUnwrap(models.firstIndex { $0["id"] as? String == "qwen3-14b-q4" })
        edit(&models[i])
        object["models"] = models
        let report = try InferenceReport.decode(try JSONSerialization.data(withJSONObject: object))
        return try XCTUnwrap(InferenceInstallOffer.make(from: report))
    }

    func testUnpinnedModelShowsTheExactReasonNotAnError() throws {
        let o = try offer { _ in }
        XCTAssertEqual(o.availability, .blocked(reason: "revision and SHA-256 not yet pinned"))
        XCTAssertEqual(o.unavailableSentence, "revision and SHA-256 not yet pinned")
    }

    func testInstallableOfferStatesSizeAndSource() throws {
        let o = try offer { $0["installable"] = true; $0["installBlockedReason"] = NSNull(); $0["downloadBytes"] = 8_589_934_592 }
        XCTAssertEqual(o.availability, .installable)
        XCTAssertEqual(o.buttonTitle, "Install \(o.displayName) (8.0 GB)")
        XCTAssertTrue(o.sizeSentence.contains("8.0 GB"))
        XCTAssertTrue(o.sourceSentence.contains("huggingface.co"))
        XCTAssertTrue(o.sourceSentence.contains("SHA-256"))
    }

    func testUnknownSizeIsNeverGuessed() throws {
        let o = try offer { $0["installable"] = true; $0["installBlockedReason"] = NSNull() }
        XCTAssertNil(o.downloadBytes)
        XCTAssertEqual(o.buttonTitle, "Install \(o.displayName)")
        XCTAssertTrue(o.sizeSentence.contains("not reported"))
    }

    func testOfferIsWithheldWhenItDoesNotFitOrDiskIsShort() throws {
        let noFit = try offer { $0["installable"] = true; $0["verdict"] = "does_not_fit" }
        XCTAssertEqual(noFit.availability, .doesNotFitThisMac)
        let elsewhere = try offer { $0["installable"] = true; $0["hostId"] = "a" }
        XCTAssertEqual(elsewhere.availability, .doesNotFitThisMac)
        let huge = try offer { $0["installable"] = true; $0["downloadBytes"] = Int64(900) << 30 }
        XCTAssertEqual(huge.availability, .notEnoughDisk)
    }

    func testMultiHostAndWebUiAreStatedUnavailable() {
        XCTAssertTrue(InferenceInstallText.unavailableLine.contains("not available"))
    }
}
