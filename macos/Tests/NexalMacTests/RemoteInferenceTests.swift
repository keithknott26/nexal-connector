import Foundation
import XCTest
@testable import NexalMac

final class RemoteInferenceTests: XCTestCase {
    func testCommandArguments() {
        let config = URL(fileURLWithPath: "/tmp/c.json")
        XCTAssertEqual(CLICommand.remoteInferenceServe(modelId: "qwen3-4b-q4", hostId: "host_1").arguments(config: config),
                       ["inference", "remote-serve", "qwen3-4b-q4", "--to", "host_1", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferenceList.arguments(config: config), ["inference", "remote-list", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferenceStop(requestId: "rinf_1").arguments(config: config),
                       ["inference", "remote-stop", "rinf_1", "--json", "--config", "/tmp/c.json"])
        XCTAssertEqual(CLICommand.remoteInferenceTest(endpoint: "http://100.86.1.2:8001/v1").arguments(config: config),
                       ["inference", "remote-test", "http://100.86.1.2:8001/v1", "--json", "--config", "/tmp/c.json"])
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
        {"id":"rinf_2","requesterHostId":"a","targetHostId":"b","modelId":"m","state":"failed","detail":"model not pinned"}

        """.utf8)
        let rows = RemoteInferenceRequest.decodeLines(data)
        XCTAssertEqual(rows.map(\.id), ["rinf_1", "rinf_2"])
        XCTAssertTrue(rows[0].isDone)
        XCTAssertEqual(rows[1].detail, "model not pinned")
    }
}
