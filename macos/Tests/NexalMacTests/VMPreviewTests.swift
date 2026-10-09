import CoreGraphics
import Foundation
import XCTest
@testable import NexalMac

final class VMPreviewTests: XCTestCase {
    // MARK: Which rows get a preview

    func testOnlyARunningVirtualMachineCanBePreviewed() {
        XCTAssertNil(VMPreviewEligibility.unavailableReason(kind: "vm", state: "running"))
        XCTAssertNil(VMPreviewEligibility.unavailableReason(kind: nil, state: "running"))
    }

    func testOtherStatesGiveAnHonestReason() {
        XCTAssertEqual(VMPreviewEligibility.unavailableReason(kind: "devcontainer", state: "running"),
                       "Development containers have no screen.")
        XCTAssertEqual(VMPreviewEligibility.unavailableReason(kind: "vm", state: "running", paused: true),
                       "Paused while this Mac sleeps.")
        XCTAssertEqual(VMPreviewEligibility.unavailableReason(kind: "vm", state: "paused"),
                       "Paused while this Mac sleeps.")
        XCTAssertEqual(VMPreviewEligibility.unavailableReason(kind: "vm", state: "stopped"),
                       "Stopped. Start it to see its screen.")
        XCTAssertEqual(VMPreviewEligibility.unavailableReason(kind: "vm", state: "provisioning"), "Still starting.")
        XCTAssertEqual(VMPreviewEligibility.unavailableReason(kind: "vm", state: "requested"), "Still starting.")
        XCTAssertEqual(VMPreviewEligibility.unavailableReason(kind: "vm", state: "stopping"), "Shutting down.")
        XCTAssertEqual(VMPreviewEligibility.unavailableReason(kind: "vm", state: "failed"), "Failed to start.")
        XCTAssertEqual(VMPreviewEligibility.unavailableReason(kind: "vm", state: nil), "Not running.")
        XCTAssertEqual(VMPreviewEligibility.unavailableReason(kind: "vm", state: "unheard-of"), "Not running.")
    }

    // MARK: Credentials from the connector

    private func reply(_ json: String) throws -> SandboxConnectReply {
        try JSONDecoder().decode(SandboxConnectReply.self, from: Data(json.utf8))
    }

    @MainActor
    func testEndpointFromConnectReply() throws {
        let endpoint = try ThrowawayHosting.vncEndpoint(
            from: reply(#"{"kind":"vnc","host":"100.64.0.9","port":5901,"password":"s3cret"}"#))
        XCTAssertEqual(endpoint, VNCEndpoint(host: "100.64.0.9", port: 5901, password: "s3cret"))
        // No port means the standard one, as for the Screen Sharing hand-off.
        XCTAssertEqual(try ThrowawayHosting.vncEndpoint(from: reply(#"{"host":"vm.mesh","password":"p"}"#)).port, 5900)
    }

    @MainActor
    func testEndpointRefusesUnusableReplies() throws {
        for json in [#"{"host":"100.64.0.9"}"#,                                   // no password
                     #"{"host":"100.64.0.9","password":""}"#,
                     #"{"password":"p"}"#,                                         // no host
                     #"{"host":"a b;c","password":"p"}"#,                          // not a host name
                     #"{"host":"100.64.0.9","port":0,"password":"p"}"#,
                     #"{"host":"100.64.0.9","port":70000,"password":"p"}"#] {
            XCTAssertThrowsError(try ThrowawayHosting.vncEndpoint(from: reply(json)), json) { error in
                guard case ThrowawayConnectError.unusable = error else { return XCTFail("\(json): \(error)") }
            }
        }
    }

    @MainActor
    func testPendingReplyIsItsOwnError() throws {
        XCTAssertThrowsError(try ThrowawayHosting.vncEndpoint(from: reply(#"{"pending":true}"#))) { error in
            guard case ThrowawayConnectError.pending = error else { return XCTFail("\(error)") }
            XCTAssertTrue(error.localizedDescription.contains("still preparing"))
        }
    }

    func testEndpointNeverShowsItsPassword() {
        let endpoint = VNCEndpoint(host: "100.64.0.9", port: 5900, password: "hunter2")
        XCTAssertFalse("\(endpoint)".contains("hunter2"))
        XCTAssertFalse(String(describing: endpoint).contains("hunter2"))
        XCTAssertFalse(String(reflecting: endpoint).contains("hunter2"))
        XCTAssertEqual(endpoint.description, "vnc://100.64.0.9:5900 (password hidden)")
    }

    // MARK: Geometry

    func testFitRectLetterboxesAndCentres() {
        let wide = VMScreenGeometry.fitRect(content: CGSize(width: 1000, height: 500), in: CGSize(width: 400, height: 400))
        XCTAssertEqual(wide, CGRect(x: 0, y: 100, width: 400, height: 200))
        let tall = VMScreenGeometry.fitRect(content: CGSize(width: 500, height: 1000), in: CGSize(width: 400, height: 400))
        XCTAssertEqual(tall, CGRect(x: 100, y: 0, width: 200, height: 400))
        // It scales up as well as down.
        let small = VMScreenGeometry.fitRect(content: CGSize(width: 100, height: 100), in: CGSize(width: 300, height: 200))
        XCTAssertEqual(small, CGRect(x: 50, y: 0, width: 200, height: 200))
        XCTAssertEqual(VMScreenGeometry.fitRect(content: .zero, in: CGSize(width: 10, height: 10)), .zero)
        XCTAssertEqual(VMScreenGeometry.fitRect(content: CGSize(width: 10, height: 10), in: .zero), .zero)
    }

    func testMouseMappingFlipsYAndHonoursTheLetterbox() throws {
        let fb = CGSize(width: 1000, height: 500)
        let view = CGSize(width: 400, height: 400) // picture occupies y 100...300 (bottom-left origin)
        let centre = try XCTUnwrap(VMScreenGeometry.framebufferPoint(forViewPoint: CGPoint(x: 200, y: 200), viewSize: view, framebuffer: fb))
        XCTAssertEqual(centre.x, 500)
        XCTAssertEqual(centre.y, 250)
        let topLeft = try XCTUnwrap(VMScreenGeometry.framebufferPoint(forViewPoint: CGPoint(x: 0, y: 300), viewSize: view, framebuffer: fb))
        XCTAssertEqual(topLeft.x, 0)
        XCTAssertEqual(topLeft.y, 0)
        // A quarter across and a quarter down from the top of the picture.
        let quarter = try XCTUnwrap(VMScreenGeometry.framebufferPoint(forViewPoint: CGPoint(x: 100, y: 250), viewSize: view, framebuffer: fb))
        XCTAssertEqual(quarter.x, 250)
        XCTAssertEqual(quarter.y, 125)
    }

    func testMouseMappingClampsToTheScreen() throws {
        let fb = CGSize(width: 1000, height: 500)
        let view = CGSize(width: 400, height: 400)
        let past = try XCTUnwrap(VMScreenGeometry.framebufferPoint(forViewPoint: CGPoint(x: 900, y: -50), viewSize: view, framebuffer: fb))
        XCTAssertEqual(past.x, 999)
        XCTAssertEqual(past.y, 499)
        let before = try XCTUnwrap(VMScreenGeometry.framebufferPoint(forViewPoint: CGPoint(x: -20, y: 999), viewSize: view, framebuffer: fb))
        XCTAssertEqual(before.x, 0)
        XCTAssertEqual(before.y, 0)
        XCTAssertNil(VMScreenGeometry.framebufferPoint(forViewPoint: CGPoint(x: 1, y: 1), viewSize: view, framebuffer: .zero))
    }

    // MARK: Keys

    func testSpecialKeys() {
        XCTAssertEqual(RFBKeyMap.specialKeysym(forKeyCode: 0x24), 0xFF0D)   // Return
        XCTAssertEqual(RFBKeyMap.specialKeysym(forKeyCode: 0x33), 0xFF08)   // Backspace
        XCTAssertEqual(RFBKeyMap.specialKeysym(forKeyCode: 0x75), 0xFFFF)   // Forward delete
        XCTAssertEqual(RFBKeyMap.specialKeysym(forKeyCode: 0x35), 0xFF1B)   // Escape
        XCTAssertEqual(RFBKeyMap.specialKeysym(forKeyCode: 0x7B), 0xFF51)   // Left
        XCTAssertEqual(RFBKeyMap.specialKeysym(forKeyCode: 0x7E), 0xFF52)   // Up
        XCTAssertEqual(RFBKeyMap.specialKeysym(forKeyCode: 0x7A), 0xFFBE)   // F1
        XCTAssertEqual(RFBKeyMap.specialKeysym(forKeyCode: 0x6F), 0xFFC9)   // F12
        XCTAssertNil(RFBKeyMap.specialKeysym(forKeyCode: 0x00))             // "a": comes from the typed character
    }

    func testModifierKeys() {
        XCTAssertEqual(RFBKeyMap.modifierKeysym(forKeyCode: 0x38), 0xFFE1)  // Shift_L
        XCTAssertEqual(RFBKeyMap.modifierKeysym(forKeyCode: 0x3C), 0xFFE2)  // Shift_R
        XCTAssertEqual(RFBKeyMap.modifierKeysym(forKeyCode: 0x3B), 0xFFE3)  // Control_L
        XCTAssertEqual(RFBKeyMap.modifierKeysym(forKeyCode: 0x3A), 0xFFE9)  // Alt_L
        XCTAssertEqual(RFBKeyMap.modifierKeysym(forKeyCode: 0x37), 0xFFEB)  // Super_L
        XCTAssertNil(RFBKeyMap.modifierKeysym(forKeyCode: 0x39))            // Caps Lock is not forwarded
    }

    func testTypedCharactersBecomeKeysyms() {
        XCTAssertEqual(RFBKeyMap.keysym(forScalar: 0x61), 0x61)             // a
        XCTAssertEqual(RFBKeyMap.keysym(forScalar: 0x41), 0x41)             // A
        XCTAssertEqual(RFBKeyMap.keysym(forScalar: 0xE9), 0xE9)             // é, Latin-1
        XCTAssertEqual(RFBKeyMap.keysym(forScalar: 0x20AC), 0x0100_20AC)    // €
        XCTAssertEqual(RFBKeyMap.keysym(forScalar: 0x1F600), 0x0101_F600)   // emoji
        XCTAssertNil(RFBKeyMap.keysym(forScalar: 0x03))                     // control character
        XCTAssertNil(RFBKeyMap.keysym(forScalar: 0x7F))
        XCTAssertNil(RFBKeyMap.keysym(forScalar: 0xF700))                   // macOS private-use code for an arrow key
        XCTAssertNil(RFBKeyMap.keysym(forScalar: 0x110000))                 // not a scalar
    }

    // MARK: Model

    @MainActor
    func testModelReportsWhyItCouldNotConnectAndCanBeStopped() async throws {
        let model = VMPreviewModel(mode: .preview) { throw ThrowawayConnectError.unusable("The host returned nothing usable.") }
        XCTAssertEqual(model.status, .idle)
        XCTAssertTrue(model.viewOnly, "view only must be the default")
        model.start()
        XCTAssertEqual(model.status, .connecting)
        for _ in 0..<100 where model.status == .connecting {
            try await Task.sleep(nanoseconds: 20_000_000)
        }
        XCTAssertEqual(model.status, .unavailable("The host returned nothing usable."))
        XCTAssertNil(model.frame)
        model.stop()
        XCTAssertEqual(model.status, .idle)
    }

    @MainActor
    func testModelIgnoresInputUntilLive() {
        let model = VMPreviewModel(mode: .window) { throw ThrowawayConnectError.pending }
        // No connection and not live: these must simply do nothing.
        model.viewOnly = false
        model.sendKey(keysym: 0x61, down: true)
        model.sendPointer(buttons: RFBButton.left, x: 1, y: 1)
        XCTAssertEqual(model.status, .idle)
    }
}
