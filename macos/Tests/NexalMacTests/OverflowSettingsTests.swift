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

    func testSetArgumentsNeverCarryAKey() {
        let a = OverflowArguments.set(enabled: true, hardAlias: "qwen-large", maxWait: "120", maxPrompt: "x", dailyLimit: "50",
                                      gpuURL: "https://api.runpod.ai/v2/e/openai/v1", gpuModel: "Qwen/Qwen3-32B",
                                      cloudURL: "", cloudModel: "", gpuPeers: "100.64.0.2", cloudPeers: "")
        XCTAssertTrue(a.contains("--enabled=true"))
        XCTAssertEqual(a[a.firstIndex(of: "--max-prompt-tokens")! + 1], "0", "a non-number becomes 0, not a crash")
        XCTAssertTrue(a.contains("--remove-cloud"))
        XCTAssertFalse(a.contains("--remove-gpu"))
        XCTAssertFalse(a.joined().lowercased().contains("key"))
    }

    func testStatusDecodes() throws {
        let json = #"{"overflow":{"enabled":true,"gpu":{"name":"gpu","baseUrl":"https://x.example/v1","model":"m"},"gpuPeers":["*"]},"gpuKeyStored":true,"cloudKeyStored":false}"#
        let s = try JSONDecoder().decode(OverflowStatus.self, from: Data(json.utf8))
        XCTAssertTrue(s.overflow.enabled)
        XCTAssertTrue(s.gpuKeyStored)
        XCTAssertEqual(s.overflow.gpu?.model, "m")
    }
}
