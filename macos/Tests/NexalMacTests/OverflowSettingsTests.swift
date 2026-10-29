import Foundation
import XCTest
@testable import NexalMac

final class OverflowSettingsTests: XCTestCase {
    func testCommandArguments() {
        let config = URL(fileURLWithPath: "/tmp/c.json")
        XCTAssertEqual(CLICommand.overflow(action: "status", args: []).arguments(config: config),
                       ["overflow", "status", "--config", config.path])
        XCTAssertEqual(CLICommand.overflow(action: "set-key", args: ["--tier", "gpu"]).arguments(config: config),
                       ["overflow", "set-key", "--tier", "gpu", "--config", config.path])
    }

    func testOffloadArguments() {
        XCTAssertEqual(OverflowArguments.offload(mode: .off, maxWait: "120", maxPrompt: "6000"),
                       ["--enabled=false", "--always=false", "--max-wait-seconds", "0", "--max-prompt-tokens", "0"])
        XCTAssertEqual(OverflowArguments.offload(mode: .busy, maxWait: "90", maxPrompt: "6000"),
                       ["--enabled=true", "--always=false", "--max-wait-seconds", "90", "--max-prompt-tokens", "0"])
        XCTAssertEqual(OverflowArguments.offload(mode: .large, maxWait: "90", maxPrompt: "x"),
                       ["--enabled=true", "--always=false", "--max-wait-seconds", "0", "--max-prompt-tokens", "0"])
        XCTAssertTrue(OverflowArguments.offload(mode: .always, maxWait: "1", maxPrompt: "1").contains("--always=true"))
    }

    func testProviderArgumentsNeverCarryAKey() {
        let a = OverflowArguments.providers(hardAlias: "qwen-large", dailyLimit: "-5", gpuURL: "https://api.runpod.ai/v2/e/openai/v1",
                                            gpuModel: "Qwen/Qwen3-32B", cloudURL: "", cloudModel: "", gpuPeers: "100.64.0.2", cloudPeers: "")
        XCTAssertEqual(a[a.firstIndex(of: "--daily-limit")! + 1], "0", "a negative number becomes 0")
        XCTAssertTrue(a.contains("--remove-cloud"))
        XCTAssertFalse(a.contains("--remove-gpu"))
        XCTAssertFalse(a.joined().lowercased().contains("key"))
    }

    func testModeFromStatus() {
        func s(_ enabled: Bool, always: Bool? = nil, wait: Int? = nil, prompt: Int? = nil) -> OverflowStatus.Settings {
            .init(enabled: enabled, always: always, hardAlias: nil, maxWaitSeconds: wait, maxPromptTokens: prompt,
                  dailyLimit: nil, gpu: nil, cloud: nil, gpuPeers: nil, cloudPeers: nil)
        }
        XCTAssertEqual(OverflowMode.from(s(false)), .off)
        XCTAssertEqual(OverflowMode.from(s(true, always: true)), .always)
        XCTAssertEqual(OverflowMode.from(s(true, wait: 60)), .busy)
        XCTAssertEqual(OverflowMode.from(s(true, prompt: 5000)), .large)
        XCTAssertEqual(OverflowMode.from(s(true, wait: 60, prompt: 5000)), .busyOrLarge)
    }

    func testStatusDecodes() throws {
        let json = #"{"overflow":{"enabled":true,"gpu":{"name":"gpu","baseUrl":"https://x.example/v1","model":"m"},"gpuPeers":["*"]},"gpuKeyStored":true,"cloudKeyStored":false}"#
        let s = try JSONDecoder().decode(OverflowStatus.self, from: Data(json.utf8))
        XCTAssertTrue(s.overflow.enabled)
        XCTAssertTrue(s.gpuKeyStored)
        XCTAssertEqual(s.overflow.gpu?.model, "m")
    }
}
