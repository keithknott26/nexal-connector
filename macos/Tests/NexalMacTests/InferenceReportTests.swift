import Foundation
import XCTest
@testable import NexalMac

final class InferenceReportTests: XCTestCase {
    // MARK: Helpers

    private func report(_ edit: ((inout [String: Any]) -> Void)? = nil) throws -> InferenceReport {
        var data = Data(Self.fixture.utf8)
        if let edit {
            var object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
            edit(&object)
            data = try JSONSerialization.data(withJSONObject: object)
        }
        return try InferenceReport.decode(data)
    }

    private func presentation(_ edit: ((inout [String: Any]) -> Void)? = nil) throws -> InferencePresentation {
        InferencePresentation(try report(edit))
    }

    // MARK: Decoding

    func testDecodesTheConnectorsReport() throws {
        let r = try report()
        XCTAssertEqual(r.schemaVersion, 1)
        XCTAssertEqual(r.machines.map(\.name), ["MacBook", "Mini-a", "Mini-b"])
        XCTAssertEqual(r.models.map(\.verdict), [.doesNotFit, .fitsShardedOnly, .runsNow, .fitsSingleHostBlocked, .runsNow])
        XCTAssertEqual(r.links[1].bandwidthMbps, 900)
        XCTAssertEqual(r.links[1].pathFlapsLastHour, 7)
        // Not measured means absent, never zero.
        XCTAssertNil(r.links[0].bandwidthMbps)
        XCTAssertEqual(r.links[0].latencyMs, 2)
        XCTAssertEqual(r.sharing.multiHostExecution, "unavailable")
        XCTAssertEqual(r.recommendation.modelId, "qwen3-14b-q4")
    }

    func testRefusesUnknownFormatsAndGarbage() throws {
        XCTAssertThrowsError(try report { $0["schemaVersion"] = 2 }) {
            XCTAssertEqual($0 as? InferenceReportError, .unsupportedSchema(2))
        }
        XCTAssertThrowsError(try InferenceReport.decode(Data("not json".utf8))) {
            XCTAssertEqual($0 as? InferenceReportError, .unreadable)
        }
        XCTAssertThrowsError(try InferenceReport.decode(Data(#"{"schemaVersion":1}"#.utf8))) {
            XCTAssertEqual($0 as? InferenceReportError, .unreadable)
        }
        XCTAssertNotNil(InferenceReportError.unsupportedSchema(2).errorDescription)
    }

    func testUnknownVerdictsAndSeveritiesStillDecode() throws {
        let r = try report { object in
            var models = object["models"] as? [[String: Any]] ?? []
            models[0]["verdict"] = "brand_new_verdict"
            object["models"] = models
            var blockers = object["blockers"] as? [[String: Any]] ?? []
            blockers[0]["severity"] = "critical"
            object["blockers"] = blockers
        }
        XCTAssertEqual(r.models[0].verdict, .other("brand_new_verdict"))
        let p = InferencePresentation(r)
        XCTAssertEqual(p.modelRows[0].verdictLabel, "Unknown result")
        XCTAssertEqual(p.modelRows[0].tone, .neutral)
        XCTAssertEqual(p.blockerRows.last?.severityLabel, "Note")
    }

    // MARK: Wording

    func testMemoryFormattingIsLocaleIndependent() {
        XCTAssertEqual(InferencePresentation.memory(UInt64(16) << 30), "16.0 GB")
        XCTAssertEqual(InferencePresentation.memory(Int64(-5)), "0.0 GB")
        XCTAssertEqual(InferencePresentation.memory(UInt64(1_610_612_736)), "1.5 GB")
    }

    func testMachineRowsStateWhatIsEstimated() throws {
        let rows = try presentation().machineRows
        XCTAssertEqual(rows[0].title, "MacBook (this Mac)")
        XCTAssertEqual(rows[0].memoryLine, "24.0 GB memory · 16.0 GB usable for a model")
        XCTAssertEqual(rows[0].runtimeLine, "MLX runtime ready")
        XCTAssertTrue(rows[0].usable)
        // Another Mac's owner limits are assumed, and the row says so.
        XCTAssertTrue(rows[1].memoryLine.contains("estimated"))
        XCTAssertEqual(rows[1].runtimeLine, "MLX runtime cannot be checked from here")
        XCTAssertEqual(rows[1].diskLine, "300.0 GB free disk")
    }

    func testOfflineOrUnusableMacsShowTheirReasons() throws {
        let rows = try presentation { object in
            var machines = object["machines"] as? [[String: Any]] ?? []
            machines[1]["eligible"] = false
            machines[1]["online"] = false
            machines[1]["reasons"] = ["Offline or asleep."]
            object["machines"] = machines
        }.machineRows
        XCTAssertFalse(rows[1].usable)
        XCTAssertEqual(rows[1].tone, .bad)
        XCTAssertEqual(rows[1].reasons, ["Offline or asleep."])
    }

    func testLinkRowsShowUnmeasuredRelayedAndUnstable() throws {
        let rows = try presentation().linkRows
        XCTAssertEqual(rows[0].qualityLabel, "Fair link")
        XCTAssertTrue(rows[0].detail.contains("bandwidth not measured"))
        XCTAssertTrue(rows[0].detail.contains("Direct, same network"))
        XCTAssertEqual(rows[1].qualityLabel, "Unstable link")
        XCTAssertEqual(rows[1].tone, .bad)
        XCTAssertTrue(rows[1].detail.contains("Through a relay"))
        XCTAssertTrue(rows[1].detail.contains("900 Mbps"))
        XCTAssertTrue(rows[1].detail.contains("switched path 7 times in the last hour"))
        XCTAssertTrue(rows[1].detail.contains("post-quantum protected"))
    }

    func testRDMAWordingComesFromTheExistingPresentation() throws {
        let expected = RDMAPresentation(capability: .notReported(sourceName: "this check")).label
        XCTAssertTrue(expected.contains("Unknown"))
        for row in try presentation().linkRows { XCTAssertEqual(row.transport, expected) }
    }

    func testModelRowsLabelToneAndRecommendation() throws {
        let rows = try presentation().modelRows
        XCTAssertEqual(rows.map(\.verdictLabel), [
            "Does not fit",
            "Only when split — not runnable yet",
            "Runs now on MacBook",
            "Fits, but blocked",
            "Runs now on MacBook",
        ])
        XCTAssertEqual(rows.map(\.tone), [.bad, .caution, .good, .caution, .good])
        XCTAssertEqual(rows.filter(\.isRecommended).map(\.id), ["qwen3-14b-q4"])
        XCTAssertEqual(rows.filter(\.runnableNow).count, 2)
        // A split model lists its parts and warns about the bad link in the layout.
        let split = rows[1]
        XCTAssertTrue(split.detail.contains { $0.hasPrefix("Part 1 on ") })
        XCTAssertTrue(split.detail.contains("A link in this layout is relayed, unstable or down."))
    }

    func testRecommendationCard() throws {
        let card = try XCTUnwrap(presentation().recommendationCard)
        XCTAssertTrue(card.title.hasPrefix("Most advanced model you can run: "))
        XCTAssertTrue(card.title.hasSuffix(" on MacBook"))
        XCTAssertTrue(card.runnableNow)
        let text = card.howToUse.joined(separator: "\n")
        XCTAssertTrue(text.contains("no web page"))
        XCTAssertTrue(text.contains("nexal-mlx-job"))
        XCTAssertFalse(text.contains("nexal infer "))
    }

    func testRecommendationThatCannotRunYetSaysSo() throws {
        let card = try XCTUnwrap(presentation { object in
            var rec = object["recommendation"] as? [String: Any] ?? [:]
            rec["runnableNow"] = false
            object["recommendation"] = rec
        }.recommendationCard)
        XCTAssertTrue(card.title.hasPrefix("Largest model your hardware can hold: "))
        XCTAssertFalse(card.runnableNow)
    }

    func testNoRecommendationWhenNothingFits() throws {
        let p = try presentation { object in
            object["recommendation"] = ["runnableNow": false, "reason": "No catalog model fits on any single Mac.", "howToUse": [String]()] as [String: Any]
        }
        XCTAssertNil(p.recommendationCard)
        XCTAssertTrue(p.modelRows.allSatisfy { !$0.isRecommended })
    }

    func testBlockersAreOrderedBlockerWarningNote() throws {
        func blocker(_ code: String, _ severity: String) -> [String: Any] {
            ["code": code, "severity": severity, "scope": "all", "title": code, "detail": "d", "fix": "f"]
        }
        let rows = try presentation { object in
            object["blockers"] = [blocker("n1", "info"), blocker("w1", "warning"), blocker("b1", "blocker"), blocker("n2", "info"), blocker("b2", "blocker")]
        }.blockerRows
        XCTAssertEqual(rows.map(\.title), ["b1", "b2", "w1", "n1", "n2"])
        XCTAssertEqual(rows.map(\.severityLabel), ["Blocker", "Blocker", "Warning", "Note", "Note"])
    }

    func testEveryBlockerCarriesAFix() throws {
        for row in try presentation().blockerRows {
            XCTAssertFalse(row.fix.isEmpty, row.title)
        }
    }

    func testSharingStatementIsTheConnectorsOwnAndNeverOverclaims() throws {
        let p = try presentation()
        XCTAssertEqual(p.sharingStatement, p.report.sharing.statement)
        XCTAssertTrue(p.sharingStatement.contains("multi-host execution is not available yet"))
        XCTAssertEqual(p.report.sharing.multiHostExecution, "unavailable")
    }

    func testGapsAndHeadlinePassThrough() throws {
        let p = try presentation()
        XCTAssertEqual(p.gapLines, p.report.gaps)
        XCTAssertFalse(p.gapLines.isEmpty)
        XCTAssertEqual(p.report.headline, "Best model you can run now: Qwen3-14B (4-bit group-64) on MacBook.")
    }

    // MARK: Wording

    func testButtonTitleAndNoFakeInstallControl() {
        XCTAssertEqual(InferencePresentation.buttonTitle, "Add AI Inference Model")
        XCTAssertFalse(InferencePresentation.intro.contains("nothing is installed"))
    }

    // MARK: CLI contract

    func testPreflightCommandArguments() {
        let config = URL(fileURLWithPath: "/Users/test/Library/Application Support/Nexal/config.json")
        XCTAssertEqual(CLICommand.inferencePreflight.arguments(config: config),
                       ["inference", "preflight", "--json", "--config", config.path])
        XCTAssertGreaterThanOrEqual(CLICommand.inferencePreflight.timeLimit, 30)
    }

    // MARK: Fixture

    /// Real output of the Go engine (two peers, one relayed and flapping), with
    /// the model list trimmed to one of each verdict.
    static let fixture = #"""
{
 "schemaVersion": 1,
 "generatedAt": "2026-10-09T12:00:00Z",
 "headline": "Best model you can run now: Qwen3-14B (4-bit group-64) on MacBook.",
 "machines": [
  {
   "id": "self",
   "name": "MacBook",
   "isSelf": true,
   "online": true,
   "chip": "Apple M4 Max",
   "os": "macOS 26.2",
   "totalMemoryBytes": 25769803776,
   "availableMemoryBytes": 21474836480,
   "approvedMemoryBytes": 17179869184,
   "ownerReserveBytes": 2147483648,
   "headroomBytes": 19327352832,
   "usableBytes": 17179869184,
   "admissionSource": "local-policy",
   "diskFreeBytes": 536870912000,
   "diskKnown": true,
   "diskReserveBytes": 0,
   "runtime": {
    "state": "healthy",
    "detail": "ok",
    "version": "0.2.0",
    "mlxVersion": "0.29.3",
    "mlxLmVersion": "0.28.4",
    "distributedExecution": "disabled",
    "verified": true
   },
   "eligible": true,
   "reasons": []
  },
  {
   "id": "a",
   "name": "Mini-a",
   "isSelf": false,
   "online": true,
   "chip": "Apple M4",
   "os": "macOS 26.2",
   "totalMemoryBytes": 25769803776,
   "availableMemoryBytes": 21474836480,
   "approvedMemoryBytes": 8589934592,
   "ownerReserveBytes": 6442450944,
   "headroomBytes": 15032385536,
   "usableBytes": 8589934592,
   "admissionSource": "assumed-peer-defaults",
   "diskFreeBytes": 322122547200,
   "diskKnown": true,
   "diskReserveBytes": 0,
   "runtime": {
    "state": "unverified",
    "detail": "The MLX runtime on another Mac cannot be probed from here. Run the preflight on that Mac.",
    "verified": false
   },
   "eligible": true,
   "reasons": []
  },
  {
   "id": "b",
   "name": "Mini-b",
   "isSelf": false,
   "online": true,
   "chip": "Apple M4",
   "os": "macOS 26.2",
   "totalMemoryBytes": 25769803776,
   "availableMemoryBytes": 21474836480,
   "approvedMemoryBytes": 8589934592,
   "ownerReserveBytes": 6442450944,
   "headroomBytes": 15032385536,
   "usableBytes": 8589934592,
   "admissionSource": "assumed-peer-defaults",
   "diskFreeBytes": 322122547200,
   "diskKnown": true,
   "diskReserveBytes": 0,
   "runtime": {
    "state": "unverified",
    "detail": "The MLX runtime on another Mac cannot be probed from here. Run the preflight on that Mac.",
    "verified": false
   },
   "eligible": true,
   "reasons": []
  }
 ],
 "links": [
  {
   "peerId": "a",
   "peerName": "Mini-a",
   "online": true,
   "path": "direct",
   "pathLabel": "Direct",
   "directVia": "lan",
   "latencyMs": 2,
   "pq": "protected",
   "pathFlapsLastHour": 0,
   "unstable": false,
   "transport": "tcp-over-mesh",
   "rdma": "unknown",
   "quality": "fair",
   "notes": [
    "RDMA and Thunderbolt are not reported by any layer, so they stay unknown; sharing would run over TCP.",
    "Bandwidth has not been measured yet (the connector measures it periodically)."
   ]
  },
  {
   "peerId": "b",
   "peerName": "Mini-b",
   "online": true,
   "path": "relay",
   "pathLabel": "Relay (Frankfurt)",
   "latencyMs": 2,
   "bandwidthMbps": 900,
   "pq": "protected",
   "pathFlapsLastHour": 7,
   "unstable": true,
   "transport": "tcp-over-mesh",
   "rdma": "unknown",
   "quality": "poor",
   "notes": [
    "RDMA and Thunderbolt are not reported by any layer, so they stay unknown; sharing would run over TCP.",
    "Traffic goes by relay path, not directly between the two Macs.",
    "The path switched between direct and relay 7 times in the last hour."
   ]
  }
 ],
 "models": [
  {
   "id": "qwen3-32b-q4",
   "displayName": "Qwen3-32B (4-bit group-64)",
   "paramsBillions": 32.8,
   "quantization": "4-bit group-64",
   "verdict": "does_not_fit",
   "summary": "Does not fit: needs about 22.8 GiB on one Mac (the most any one Mac can offer is 16.0 GiB), and no layout across 3 Macs fits their per-Mac headroom.",
   "requiredBytesSingleHost": 24484396218,
   "reasons": [
    "Combined usable memory is 32.0 GiB, but each Mac's share must cover its own weights, KV cache, activations and safety margin.",
    "Cannot be installed yet: revision and SHA-256 not yet pinned."
   ],
   "installable": false,
   "installBlockedReason": "revision and SHA-256 not yet pinned",
   "modelProvisioning": "Model files are not detected by this check; the owner must provision a reviewed copy."
  },
  {
   "id": "qwen3-14b-q8",
   "displayName": "Qwen3-14B (8-bit group-64)",
   "paramsBillions": 14.8,
   "quantization": "8-bit group-64",
   "verdict": "fits_sharded_only",
   "summary": "Fits only when split across 3 Macs. Not runnable yet: multi-host execution is not available.",
   "requiredBytesSingleHost": 20522768464,
   "layout": [
    {
     "rank": 0,
     "machineId": "a",
     "machineName": "Mini-a",
     "requiredBytes": 7884564598,
     "usableBytes": 8589934592
    },
    {
     "rank": 1,
     "machineId": "b",
     "machineName": "Mini-b",
     "requiredBytes": 7884564598,
     "usableBytes": 8589934592
    },
    {
     "rank": 2,
     "machineId": "self",
     "machineName": "MacBook",
     "requiredBytes": 7884564598,
     "usableBytes": 17179869184
    }
   ],
   "layoutLinksOk": false,
   "reasons": [
    "At least one Mac in the layout has a relayed, unstable or down link, so sharing would be unreliable even once it exists.",
    "Cannot be installed yet: revision and SHA-256 not yet pinned."
   ],
   "installable": false,
   "installBlockedReason": "revision and SHA-256 not yet pinned",
   "modelProvisioning": "Model files are not detected by this check; the owner must provision a reviewed copy."
  },
  {
   "id": "qwen3-14b-q4",
   "displayName": "Qwen3-14B (4-bit group-64)",
   "paramsBillions": 14.8,
   "quantization": "4-bit group-64",
   "verdict": "runs_now",
   "summary": "Runs now on MacBook (about 10.8 GiB needed).",
   "requiredBytesSingleHost": 11590804064,
   "hostId": "self",
   "hostName": "MacBook",
   "reasons": [
    "Cannot be installed yet: revision and SHA-256 not yet pinned."
   ],
   "installable": false,
   "installBlockedReason": "revision and SHA-256 not yet pinned",
   "modelProvisioning": "Model files are not detected by this check; the owner must provision a reviewed copy."
  },
  {
   "id": "qwen3-8b-q4",
   "displayName": "Qwen3-8B (4-bit group-64)",
   "paramsBillions": 8.2,
   "quantization": "4-bit group-64",
   "verdict": "fits_single_host_blocked",
   "summary": "Fits on MacBook, but its MLX runtime is not ready (not configured).",
   "requiredBytesSingleHost": 7280561648,
   "hostId": "self",
   "hostName": "MacBook",
   "reasons": [
    "Cannot be installed yet: revision and SHA-256 not yet pinned."
   ],
   "installable": false,
   "installBlockedReason": "revision and SHA-256 not yet pinned",
   "modelProvisioning": "Model files are not detected by this check; the owner must provision a reviewed copy."
  },
  {
   "id": "qwen3-4b-q4",
   "displayName": "Qwen3-4B (4-bit group-64)",
   "paramsBillions": 4,
   "quantization": "4-bit group-64",
   "verdict": "runs_now",
   "summary": "Runs now on MacBook (about 4.2 GiB needed).",
   "requiredBytesSingleHost": 4549691072,
   "hostId": "self",
   "hostName": "MacBook",
   "reasons": [
    "Cannot be installed yet: revision and SHA-256 not yet pinned."
   ],
   "installable": false,
   "installBlockedReason": "revision and SHA-256 not yet pinned",
   "modelProvisioning": "Model files are not detected by this check; the owner must provision a reviewed copy."
  }
 ],
 "recommendation": {
  "modelId": "qwen3-14b-q4",
  "displayName": "Qwen3-14B (4-bit group-64)",
  "runnableNow": true,
  "hostName": "MacBook",
  "reason": "The largest catalog model that fits MacBook and has a healthy runtime.",
  "howToUse": [
   "There is no web page or app screen for chatting with a model today. The only way to run one is the command line on the Mac that holds the model.",
   "1. On MacBook, put a reviewed, licensed copy of Qwen3-14B (4-bit group-64) in a read-only folder with a nexal-model-manifest.json (steps in runtimes/README.md, \"Local provisioning\"). The app cannot install it yet.",
   "2. Check the runtime: python3 -I /ABSOLUTE/APPROVED/nexal_mlx_entry.py probe",
   "3. Run one prompt (the separate nexal-mlx-job tool from runtimes/bridge, not a `nexal` subcommand): nexal-mlx-job --owner-policy /owner/policy.json --admission /owner/fresh-admission.json --prompt-file /owner/prompt.txt --attempt-id ATTEMPT --model-manifest-sha256 REVIEWED_DIGEST --max-tokens 256",
   "Limits: prompts up to 4096 tokens (16 KiB), answers up to 512 tokens, greedy decoding."
  ]
 },
 "sharing": {
  "singleHostExecution": "available",
  "multiHostExecution": "unavailable",
  "statement": "Sharing across peers: planned layout fits, but multi-host execution is not available yet; single-host run is available.",
  "evidence": [
   "pool.PlanMLX (internal/pool/placement.go) only plans memory placement; every plan reports executionValidated=false and it never launches MLX.",
   "pool collective + `nexal collective` (internal/pool/collective.go, cmd/nexal/collective.go) is a ring all-reduce of numbers over mutual-TLS peers: a primitive, with no model, MLX or scheduler behind it."
  ]
 },
 "blockers": [
  {
   "code": "link-unstable",
   "severity": "warning",
   "scope": "Mini-b",
   "title": "The link to Mini-b keeps switching paths",
   "detail": "It changed between direct and relay 7 times in the last hour.",
   "fix": "Put both Macs on the same network, or cable them together, so the direct path stays up."
  },
  {
   "code": "multi-host-unavailable",
   "severity": "info",
   "scope": "all",
   "title": "Splitting a model across Macs is not built yet",
   "detail": "A layout exists on paper (pool.PlanMLX), but nothing can execute it: the only multi-host code is a scalar all-sum test and an all-reduce primitive.",
   "fix": "Nothing to do today. Run a smaller model on one Mac, or use a Mac with more memory."
  },
  {
   "code": "catalog-not-pinned",
   "severity": "info",
   "scope": "catalog",
   "title": "No catalog model can be installed yet",
   "detail": "Every catalog entry is unpinned: revision and SHA-256 are not yet pinned, and the runtime refuses anything it has not pinned.",
   "fix": "The install step will arrive after the catalog hashes are reviewed and filled in. Until then, provision a checkpoint by hand as runtimes/README.md describes."
  }
 ],
 "gaps": [
  "Bandwidth to Mini-a has not been measured yet.",
  "The MLX runtime on other Macs cannot be probed remotely; run this check on each Mac.",
  "Only links from this Mac are measured; links between two peers are not visible.",
  "Whether a peer accepts compute (paused, policy) is not reported.",
  "RDMA and Thunderbolt capability is not reported by any layer (unknown, not absent).",
  "Whether model weights are already on disk is not checked."
 ],
 "assumptions": [
  "Another Mac's owner memory cap and reserve are not visible remotely. Each is assumed to have a cap of 8192 MiB (the largest the connector accepts) and a reserve of 25% of its RAM.",
  "Model sizes are estimates from parameter count and bits per weight, not measurements."
 ],
 "catalogNote": "ESTIMATE: derived from parameter count and bits per weight, not measured on hardware.",
 "surface": "There is no web UI for inference. The only surface today is the command line on the Mac that holds the model; this check is read-only and installs nothing."
}
"""#
}
