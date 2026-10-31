import Foundation
import XCTest
@testable import NexalMac

final class RemoteInferenceTests: XCTestCase {
    func testCommandArguments() {
        let config = URL(fileURLWithPath: "/tmp/c.json")
        XCTAssertEqual(CLICommand.remoteInferenceServe(modelId: "qwen3-4b-q4", hostId: "host_1").arguments(config: config),
                       ["inference", "remote-serve", "qwen3-4b-q4", "--to", "host_1", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferencePool(modelId: "big-30b", hostIds: ["h1", "h2"]).arguments(config: config),
                       ["inference", "remote-serve", "big-30b", "--pool", "h1,h2", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.inferenceReviewed.arguments(config: config), ["inference", "reviewed", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.inferenceReviewLatest(modelId: "qwen3-4b-q4").arguments(config: config), ["inference", "review-latest", "qwen3-4b-q4", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.inferenceReview(modelId: "qwen3-4b-q4", revision: String(repeating: "a", count: 40), out: "/r/reviewed-pins.json").arguments(config: config),
                       ["inference", "review", "qwen3-4b-q4", "--revision", String(repeating: "a", count: 40), "--out", "/r/reviewed-pins.json", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferenceList.arguments(config: config), ["inference", "remote-list", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferenceOffload(requestId: "rinf_1", mode: "always", maxPrompt: 0, maxWait: 0, shareGPU: false).arguments(config: config),
                       ["inference", "remote-offload", "rinf_1", "--mode", "always", "--max-prompt", "0", "--max-wait", "0", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferenceOffload(requestId: "rinf_1", mode: "always", maxPrompt: 0, maxWait: 0, shareGPU: true).arguments(config: config),
                       ["inference", "remote-offload", "rinf_1", "--mode", "always", "--max-prompt", "0", "--max-wait", "0", "--share-gpu", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferenceStats.arguments(config: config), ["inference", "remote-stats", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferenceStop(requestId: "rinf_1").arguments(config: config),
                       ["inference", "remote-stop", "rinf_1", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferenceTest(endpoint: "http://100.86.1.2:8001/v1").arguments(config: config),
                       ["inference", "remote-test", "http://100.86.1.2:8001/v1", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferenceSweep(endpoint: "http://100.86.1.2:8001/v1", calls: 20).arguments(config: config),
                       ["inference", "remote-test", "http://100.86.1.2:8001/v1", "--calls", "20", "--json", "--config", "/tmp/c.json"])
    }

    func testEndpointAndIdValidation() {
        XCTAssertTrue(RemoteInferenceID.isMeshEndpoint("http://100.86.197.234:8001/v1"))
        XCTAssertTrue(RemoteInferenceID.isMeshEndpoint("http://127.0.0.1:8002/v1"))
        for bad in ["https://100.86.1.2:8001/v1", "http://example.com:8001/v1", "http://8.8.8.8:8001/v1", "http://100.86.1.2/v1", "http://u@100.86.1.2:8001/v1"] {
            XCTAssertFalse(RemoteInferenceID.isMeshEndpoint(bad), bad)
        }
        XCTAssertTrue(RemoteInferenceID.isValid("host_abc-1"))
        XCTAssertFalse(RemoteInferenceID.isValid("a b"))
        XCTAssertFalse(RemoteInferenceID.isValid("--to"))  // must not read as a flag
    }

    func testDecodesRequestLines() {
        let data = Data("""
        {"id":"rinf_1","requesterHostId":"a","targetHostId":"b","modelId":"m","state":"ready","step":"serve","endpoint":"http://100.1.2.3:8001/v1"}
        {"id":"rinf_3","requesterHostId":"a","targetHostId":"b","modelId":"m","state":"running","step":"install","progress":0.42}
        {"id":"rinf_2","requesterHostId":"a","targetHostId":"b","modelId":"m","state":"failed","detail":"model not pinned"}

        """.utf8)
        let rows = RemoteInferenceRequest.decodeLines(data)
        XCTAssertEqual(rows.map(\.id), ["rinf_1", "rinf_3", "rinf_2"])
        XCTAssertEqual(rows[1].progress, 0.42)
        XCTAssertTrue(rows[0].isDone)
        XCTAssertEqual(rows[2].detail, "model not pinned")
    }

    func testStagesFollowTheStepsTheOtherMacReports() {
        XCTAssertEqual(RemoteInferenceStages.all.count, 6)
        XCTAssertEqual(RemoteInferenceStages.index(of: "ports"), 0)
        XCTAssertEqual(RemoteInferenceStages.index(of: "setup"), 1)
        XCTAssertEqual(RemoteInferenceStages.index(of: "install"), 3)
        XCTAssertEqual(RemoteInferenceStages.index(of: "verify"), 4)
        XCTAssertEqual(RemoteInferenceStages.index(of: "serve"), 5)
        XCTAssertNil(RemoteInferenceStages.index(of: "network"))
        XCTAssertNil(RemoteInferenceStages.index(of: nil))
    }

    func testPoolRequestsBecomeOneCard() {
        let data = Data("""
        {"id":"r1","targetHostId":"h1","modelId":"big","state":"running","step":"serve","progress":0.9,"poolId":"p1","poolRank":0,"poolSize":2}
        {"id":"r2","targetHostId":"h2","modelId":"big","state":"running","step":"install","progress":0.3,"poolId":"p1","poolRank":1,"poolSize":2}
        {"id":"r3","targetHostId":"h3","modelId":"small","state":"ready","step":"serve","endpoint":"http://100.1.2.3:8001/v1"}

        """.utf8)
        let cards = RemoteInferenceCard.cards(RemoteInferenceRequest.decodeLines(data))
        XCTAssertEqual(cards.map(\.id), ["r1", "r3"])
        XCTAssertTrue(cards[0].isPool)
        // Once the Mac that answers is serving, its own step is the news (see RemoteInferenceCard.cards).
        XCTAssertEqual(cards[0].step, "serve")
        XCTAssertEqual(cards[0].state, "running")
        XCTAssertEqual(cards[0].progress ?? 0, 0.6, accuracy: 0.001)
        XCTAssertFalse(cards[1].isPool)
        XCTAssertEqual(cards[1].endpoint, "http://100.1.2.3:8001/v1")
        XCTAssertEqual(cards[1].addresses, ["http://100.1.2.3:8001/v1"])  // an older Mac sends only the neXal address
    }

    func testPoolStepIsTheSlowestMacUntilTheLeadServes() {
        let data = Data("""
        {"id":"r1","targetHostId":"h1","modelId":"big","state":"running","step":"verify","progress":0.8,"poolId":"p1","poolRank":0,"poolSize":2}
        {"id":"r2","targetHostId":"h2","modelId":"big","state":"running","step":"install","progress":0.3,"poolId":"p1","poolRank":1,"poolSize":2}

        """.utf8)
        let cards = RemoteInferenceCard.cards(RemoteInferenceRequest.decodeLines(data))
        XCTAssertEqual(cards[0].step, "install")  // the slowest Mac sets the pace
    }

    func testPoolFailureShowsTheFailedMac() {
        let data = Data("""
        {"id":"r1","targetHostId":"h1","modelId":"big","state":"failed","step":"pool","detail":"Another Mac in the pool could not take part.","poolId":"p1","poolRank":0,"poolSize":2}
        {"id":"r2","targetHostId":"h2","modelId":"big","state":"failed","step":"install","detail":"download failed","poolId":"p1","poolRank":1,"poolSize":2}

        """.utf8)
        let card = RemoteInferenceCard.cards(RemoteInferenceRequest.decodeLines(data))[0]
        XCTAssertEqual(card.state, "failed")
        XCTAssertEqual(card.detail, "download failed")
        XCTAssertEqual(card.step, "install")
    }

    func testReadyPoolShowsTheMacThatAnswersAndItsNotice() {
        let lines = """
        {"id":"r1","targetHostId":"h1","modelId":"big","state":"ready","step":"pool","detail":"The pool lost a member (a Mac went quiet or left), so it is serving from this Mac alone.","endpoint":"http://100.86.0.1:8001/v1","poolId":"p1","poolRank":0,"poolSize":2}
        {"id":"r2","targetHostId":"h2","modelId":"big","state":"running","step":"pool","detail":"Ready to share the load.","poolId":"p1","poolRank":1,"poolSize":2}
        """
        let cards = RemoteInferenceCard.cards(RemoteInferenceRequest.decodeLines(Data(lines.utf8)))
        XCTAssertEqual(cards.count, 1)
        XCTAssertEqual(cards[0].state, "ready")
        XCTAssertTrue(cards[0].detail?.contains("lost a member") == true)
        XCTAssertTrue(RemoteInferenceStages.isWarning(cards[0].detail ?? ""))
        XCTAssertFalse(RemoteInferenceStages.isWarning("Serving big with 2 Macs sharing the load."))
    }

    func testPoolStageIndex() {
        XCTAssertEqual(RemoteInferenceStages.index(of: "pool"), 4)
        XCTAssertEqual(RemoteInferenceStages.index(of: "serve"), 5)
        XCTAssertEqual(RemoteInferenceStages.label("running", "pool"), "waiting for the pool")
    }

    func testReviewLatestDecodesAndOnlyOpensHuggingFace() throws {
        let json = #"{"modelId":"m","hfRepo":"a/b","revision":"abc","license":"apache-2.0","gated":false,"commitUrl":"https://huggingface.co/a/b/commit/abc","reviewedRevision":"","upToDate":false}"#
        let l = try JSONDecoder().decode(ReviewLatest.self, from: Data(json.utf8))
        XCTAssertEqual(l.commitPage?.host, "huggingface.co")
        let evil = json.replacingOccurrences(of: "https://huggingface.co", with: "https://evil.example")
        XCTAssertNil(try JSONDecoder().decode(ReviewLatest.self, from: Data(evil.utf8)).commitPage)
    }

    func testEveryAddressIsListedAndLabelled() {
        let line = """
        {"id":"r9","targetHostId":"h9","modelId":"m","state":"ready","step":"serve","endpoint":"http://100.86.56.136:8015/v1","endpoints":["http://100.86.56.136:8015/v1","http://192.168.1.20:8015/v1","http://127.0.0.1:8015/v1"]}
        """
        let cards = RemoteInferenceCard.cards(RemoteInferenceRequest.decodeLines(Data(line.utf8)))
        XCTAssertEqual(cards[0].addresses.count, 3)
        XCTAssertEqual(cards[0].addresses.map(RemoteInferenceID.kind), ["neXal network", "LAN", "That Mac only"])
    }

    func testTheOffloadModesMapToTheRemotePolicy() {
        XCTAssertTrue(OverflowArguments.remote(mode: .always, maxWait: "120", maxPrompt: "6000") == ("always", 0, 0))
        XCTAssertTrue(OverflowArguments.remote(mode: .off, maxWait: "120", maxPrompt: "6000") == ("off", 0, 0))
        XCTAssertTrue(OverflowArguments.remote(mode: .large, maxWait: "120", maxPrompt: "6000") == ("large", 6000, 0))
        XCTAssertTrue(OverflowArguments.remote(mode: .busyOrLarge, maxWait: "9999", maxPrompt: "6000") == ("large", 6000, 3600))
    }

    func testARequestCarriesTheOffloadPolicyAndTheAnswer() {
        let line = #"{"id":"rinf_1","targetHostId":"m4","modelId":"qwen3-4b-q4","state":"ready","offload":{"mode":"always","maxPromptTokens":0,"maxWaitSeconds":0,"setAt":"x"},"offloadNote":"Applied: every request goes to RunPod."}"#
        let r = RemoteInferenceRequest.decodeLines(Data(line.utf8))
        XCTAssertEqual(r.first?.offload?.mode, "always")
        XCTAssertEqual(r.first?.offloadNote, "Applied: every request goes to RunPod.")
    }
}
