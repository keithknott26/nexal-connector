import Foundation
import XCTest
@testable import NexalMac

/// RFBProtocol against canned server byte streams. Every stream is also fed split in two at every
/// offset and one byte at a time: the parser must produce the same events whatever the chunking.
final class RFBProtocolTests: XCTestCase {
    // MARK: Wire helpers

    private func be16(_ v: Int) -> [UInt8] { [UInt8((v >> 8) & 0xFF), UInt8(v & 0xFF)] }
    private func be32(_ v: UInt32) -> [UInt8] {
        [UInt8(v >> 24), UInt8((v >> 16) & 0xFF), UInt8((v >> 8) & 0xFF), UInt8(v & 0xFF)]
    }
    private func str32(_ s: String) -> [UInt8] { be32(UInt32(s.utf8.count)) + Array(s.utf8) }
    /// Concatenation without long `+` chains, which the type checker handles slowly.
    private func cat(_ parts: [UInt8]...) -> [UInt8] { parts.flatMap { $0 } }

    private let version = Array("RFB 003.008\n".utf8)
    private let pixelFormat: [UInt8] = [32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0]

    private func serverInit(_ w: Int, _ h: Int, name: String = "vm") -> [UInt8] {
        be16(w) + be16(h) + pixelFormat + str32(name)
    }

    /// Version, one security type (None), success, ServerInit.
    private func noneHandshake(_ w: Int, _ h: Int, name: String = "vm") -> [UInt8] {
        cat(version, [1, 1], be32(0), serverInit(w, h, name: name))
    }

    private func rectHeader(_ x: Int, _ y: Int, _ w: Int, _ h: Int, _ encoding: Int32) -> [UInt8] {
        be16(x) + be16(y) + be16(w) + be16(h) + be32(UInt32(bitPattern: encoding))
    }

    private func update(_ rects: [[UInt8]]) -> [UInt8] {
        [0, 0] + be16(rects.count) + rects.flatMap { $0 }
    }

    /// A pixel whose first (blue) byte is `n`: distinct per n, easy to read in assertions.
    private func px(_ n: Int) -> [UInt8] { [UInt8(n), 0, 0, 0] }

    private func startRequest(_ w: Int, _ h: Int) -> [UInt8] {
        RFBEncode.setPixelFormat() + RFBEncode.setEncodings(RFBEncode.supportedEncodings)
            + RFBEncode.framebufferUpdateRequest(incremental: false, x: 0, y: 0, width: w, height: h)
    }

    // MARK: Feeding helpers

    private struct Run {
        var events: [RFBEvent]
        var parser: RFBParser
    }

    private func feed(_ chunks: [[UInt8]], password: String? = nil) throws -> Run {
        var parser = RFBParser(password: password)
        var events: [RFBEvent] = []
        for chunk in chunks { events += try parser.feed(chunk) }
        return Run(events: events, parser: parser)
    }

    /// Feeds the stream whole, split at every offset, and byte by byte; all must agree. Returns the whole run.
    @discardableResult
    private func feedEverySplit(_ bytes: [UInt8], password: String? = nil,
                                file: StaticString = #filePath, line: UInt = #line) throws -> Run {
        let whole = try feed([bytes], password: password)
        for cut in 0...bytes.count {
            let run = try feed([Array(bytes[0..<cut]), Array(bytes[cut...])], password: password)
            XCTAssertEqual(run.events, whole.events, "split at \(cut)", file: file, line: line)
            XCTAssertEqual(run.parser.framebuffer, whole.parser.framebuffer, "split at \(cut)", file: file, line: line)
        }
        let single = try feed(bytes.map { [$0] }, password: password)
        XCTAssertEqual(single.events, whole.events, "byte by byte", file: file, line: line)
        XCTAssertEqual(single.parser.framebuffer, whole.parser.framebuffer, "byte by byte", file: file, line: line)
        return whole
    }

    /// The error the stream ends in, at every split offset (it must not depend on chunking).
    private func assertFails(_ bytes: [UInt8], with expected: RFBError, password: String? = nil,
                             file: StaticString = #filePath, line: UInt = #line) {
        for cut in 0...bytes.count {
            var thrown: Error?
            do { _ = try feed([Array(bytes[0..<cut]), Array(bytes[cut...])], password: password) } catch { thrown = error }
            XCTAssertEqual(thrown as? RFBError, expected, "split at \(cut)", file: file, line: line)
        }
    }

    // MARK: Encoders

    func testSetPixelFormatBytes() {
        XCTAssertEqual(RFBEncode.setPixelFormat(),
                       [0, 0, 0, 0,
                        32, 24, 0, 1,
                        0, 255, 0, 255, 0, 255,
                        16, 8, 0,
                        0, 0, 0])
    }

    func testSetEncodingsBytes() {
        XCTAssertEqual(RFBEncode.setEncodings(RFBEncode.supportedEncodings),
                       [2, 0, 0, 5,
                        0, 0, 0, 5,       // Hextile
                        0, 0, 0, 1,       // CopyRect
                        0, 0, 0, 0,       // Raw
                        0xFF, 0xFF, 0xFF, 0x21,   // DesktopSize -223
                        0xFF, 0xFF, 0xFE, 0xCC])  // ExtendedDesktopSize -308
    }

    func testFramebufferUpdateRequestBytes() {
        XCTAssertEqual(RFBEncode.framebufferUpdateRequest(incremental: true, x: 1, y: 2, width: 0x0102, height: 0x0304),
                       [3, 1, 0, 1, 0, 2, 1, 2, 3, 4])
        XCTAssertEqual(RFBEncode.framebufferUpdateRequest(incremental: false, x: 0, y: 0, width: 800, height: 600),
                       [3, 0, 0, 0, 0, 0, 0x03, 0x20, 0x02, 0x58])
    }

    func testKeyEventBytes() {
        XCTAssertEqual(RFBEncode.keyEvent(down: true, keysym: 0xFF0D), [4, 1, 0, 0, 0, 0, 0xFF, 0x0D])
        XCTAssertEqual(RFBEncode.keyEvent(down: false, keysym: 0x61), [4, 0, 0, 0, 0, 0, 0, 0x61])
        XCTAssertEqual(RFBEncode.keyEvent(down: true, keysym: 0x0101_F600), [4, 1, 0, 0, 0x01, 0x01, 0xF6, 0x00])
    }

    func testPointerEventBytes() {
        XCTAssertEqual(RFBEncode.pointerEvent(buttons: RFBButton.left | RFBButton.right, x: 300, y: 7), [5, 5, 1, 0x2C, 0, 7])
        XCTAssertEqual(RFBEncode.pointerEvent(buttons: RFBButton.wheelUp, x: 0, y: 0), [5, 8, 0, 0, 0, 0])
        // Coordinates outside 16 bits are clamped, never wrapped.
        XCTAssertEqual(RFBEncode.pointerEvent(buttons: 0, x: 70_000, y: -5), [5, 0, 0xFF, 0xFF, 0, 0])
    }

    // MARK: Handshake

    func testHandshakeWithNoSecurity() throws {
        let run = try feedEverySplit(noneHandshake(4, 3, name: "Ubuntu desktop"))
        XCTAssertEqual(run.events, [
            .send(version),                       // we answer 3.8
            .send([1]),                           // chose security type None
            .send([1]),                           // ClientInit: shared
            .serverInit(width: 4, height: 3, name: "Ubuntu desktop"),
            .send(startRequest(4, 3)),
        ])
        XCTAssertTrue(run.parser.isLive)
        XCTAssertEqual(run.parser.framebuffer.width, 4)
        XCTAssertEqual(run.parser.framebuffer.height, 3)
        XCTAssertEqual(run.parser.desktopName, "Ubuntu desktop")
    }

    func testNewerMinorVersionIsAnsweredWith38() throws {
        // Apple's server announces 3.889.
        let bytes = cat(Array("RFB 003.889\n".utf8), [1, 1], be32(0), serverInit(2, 2))
        let run = try feedEverySplit(bytes)
        XCTAssertEqual(run.events.first, .send(version))
    }

    func testBadVersionIsRejected() {
        for line in ["RFB 003.003\n", "RFB 003.007\n", "RFB 004.000\n", "HTTP/1.1 400", "RFB 3.8\n\n\n\n\n", "SSH-2.0-Open"] {
            var parser = RFBParser(password: nil)
            XCTAssertThrowsError(try parser.feed(Array(line.utf8)), line) { error in
                guard case RFBError.badVersion = error else { return XCTFail("\(line): \(error)") }
            }
        }
        // Fewer than 12 bytes is just not enough yet.
        var parser = RFBParser(password: nil)
        XCTAssertEqual(try parser.feed(Array("RFB 003".utf8)), [])
    }

    func testParserStaysDeadAfterAnError() {
        var parser = RFBParser(password: nil)
        XCTAssertThrowsError(try parser.feed(Array("HTTP/1.1 400".utf8)))
        XCTAssertThrowsError(try parser.feed(version))
    }

    func testSecurityFailureCarriesTheServersReason() {
        assertFails(cat(version, [0], str32("Too many authentication failures")),
                    with: .securityFailed("Too many authentication failures"))
        // An absurd reason length does not make the parser wait for it.
        assertFails(cat(version, [0], be32(100_000)), with: .securityFailed("no reason given"))
    }

    func testServerTextIsMadeSafe() {
        let nasty = "bad\u{1B}[31m\u{7}text"
        assertFails(cat(version, [0], str32(nasty)), with: .securityFailed("bad[31mtext"))
    }

    func testUnsupportedSecurityTypesAreNamed() {
        assertFails(cat(version, [1, 30], be32(0)), with: .unsupportedSecurity([30]))
        assertFails(cat(version, [2, 18, 19]), with: .unsupportedSecurity([18, 19]))
        XCTAssertTrue(RFBError.unsupportedSecurity([30]).localizedDescription.contains("Apple Remote Desktop"))
        XCTAssertTrue(RFBError.unsupportedSecurity([19]).localizedDescription.contains("VeNCrypt"))
    }

    func testPasswordOnlyServerWithoutAPasswordSaysSo() {
        assertFails(cat(version, [1, 2]), with: .needsPassword)
        assertFails(cat(version, [1, 2]), with: .needsPassword, password: "")
    }

    func testSecurityChoicePrefersAPasswordWhenOneIsGiven() throws {
        let offer = cat(version, [2, 1, 2])
        XCTAssertEqual(try feed([offer]).events, [.send(version), .send([1])])
        XCTAssertEqual(try feed([offer], password: "pw").events, [.send(version), .send([2])])
    }

    func testVNCAuthenticationAnswersTheChallenge() throws {
        let challenge = (0..<16).map { UInt8($0) }
        let expected = [UInt8](hex: "b866924125c8eebb9debc1db61c538e2")
        let bytes = cat(version, [1, 2], challenge, be32(0), serverInit(4, 3))
        let run = try feedEverySplit(bytes, password: "password")
        XCTAssertEqual(run.events, [
            .send(version),
            .send([2]),
            .send(expected),
            .send([1]),
            .serverInit(width: 4, height: 3, name: "vm"),
            .send(startRequest(4, 3)),
        ])
    }

    func testAuthenticationFailureCarriesTheReason() {
        let challenge = [UInt8](repeating: 7, count: 16)
        assertFails(cat(version, [1, 2], challenge, be32(1), str32("Authentication failed")),
                    with: .authFailed("Authentication failed"), password: "wrong")
        assertFails(cat(version, [1, 2], challenge, be32(1), str32("")),
                    with: .authFailed("authentication failed"), password: "wrong")
        // The reason never contains the password we sent.
        XCTAssertFalse(RFBError.authFailed("Authentication failed").localizedDescription.contains("wrong"))
    }

    func testFailedResultWithoutReasonYetJustWaits() throws {
        var parser = RFBParser(password: nil)
        let events = try parser.feed(cat(version, [1, 1], be32(1)))
        XCTAssertEqual(events, [.send(version), .send([1])])
        XCTAssertFalse(parser.isLive)
    }

    // MARK: Auth primitives

    func testBitReversal() {
        XCTAssertEqual(RFBAuth.reverseBits(0x01), 0x80)
        XCTAssertEqual(RFBAuth.reverseBits(0x80), 0x01)
        XCTAssertEqual(RFBAuth.reverseBits(0x70), 0x0E) // "p"
        XCTAssertEqual(RFBAuth.reverseBits(0xFF), 0xFF)
        XCTAssertEqual(RFBAuth.reverseBits(0x00), 0x00)
        for b in 0...255 { XCTAssertEqual(RFBAuth.reverseBits(RFBAuth.reverseBits(UInt8(b))), UInt8(b)) }
    }

    func testDESKeyIsFirstEightBytesBitReversedAndZeroPadded() {
        XCTAssertEqual(RFBAuth.desKey(forPassword: "password"), [UInt8](hex: "0e86ceceeef64e26"))
        XCTAssertEqual(RFBAuth.desKey(forPassword: "secret"), [UInt8](hex: "cea6c64ea62e0000"))
        XCTAssertEqual(RFBAuth.desKey(forPassword: "abcdefghijk"), RFBAuth.desKey(forPassword: "abcdefgh"))
        XCTAssertEqual(RFBAuth.desKey(forPassword: ""), [UInt8](repeating: 0, count: 8))
    }

    /// Vectors computed with OpenSSL's DES-ECB (checked first against the classic FIPS 46 example,
    /// key 133457799BBCDFF1 / plaintext 0123456789ABCDEF -> 85E813540F0AB405), then bit-reversing
    /// the key as RFB 6.2.2 requires. They do not come from this code.
    func testDESKnownVectors() {
        XCTAssertEqual(RFBAuth.desEncrypt(block: [UInt8](hex: "0123456789ABCDEF"), key: [UInt8](hex: "133457799BBCDFF1")),
                       [UInt8](hex: "85E813540F0AB405"))

        let sequential = (0..<16).map { UInt8($0) }
        XCTAssertEqual(RFBAuth.response(challenge: sequential, password: "password"),
                       [UInt8](hex: "b866924125c8eebb9debc1db61c538e2"))
        XCTAssertEqual(RFBAuth.response(challenge: [UInt8](hex: "00112233445566778899aabbccddeeff"), password: "secret"),
                       [UInt8](hex: "f19b50471f60f42298e5c0147db50e1e"))
        // Only the first eight characters of the password count.
        XCTAssertEqual(RFBAuth.response(challenge: sequential, password: "abcdefghijk"),
                       [UInt8](hex: "eae3a1cb74ca6daac183f66460190bb5"))
        XCTAssertEqual(RFBAuth.response(challenge: sequential, password: "abcdefgh"),
                       [UInt8](hex: "eae3a1cb74ca6daac183f66460190bb5"))
    }

    func testResponseRejectsAWrongSizedChallenge() {
        XCTAssertNil(RFBAuth.response(challenge: [1, 2, 3], password: "pw"))
        XCTAssertNil(RFBAuth.desEncrypt(block: [1, 2, 3], key: [UInt8](repeating: 0, count: 8)))
    }

    // MARK: Raw

    func testRawRectangle() throws {
        // 4 x 3 screen; a 2 x 2 raw rectangle at (1, 1).
        let raw = rectHeader(1, 1, 2, 2, 0) + px(1) + px(2) + px(3) + px(4)
        let run = try feedEverySplit(noneHandshake(4, 3) + update([raw]))
        XCTAssertEqual(run.events.last, .updateFinished([RFBRect(x: 1, y: 1, width: 2, height: 2)]))
        let fb = run.parser.framebuffer
        XCTAssertEqual(fb.pixelBytes(x: 1, y: 1), px(1))
        XCTAssertEqual(fb.pixelBytes(x: 2, y: 1), px(2))
        XCTAssertEqual(fb.pixelBytes(x: 1, y: 2), px(3))
        XCTAssertEqual(fb.pixelBytes(x: 2, y: 2), px(4))
        XCTAssertEqual(fb.pixelBytes(x: 0, y: 0), px(0))
        XCTAssertEqual(fb.pixelBytes(x: 3, y: 2), px(0))
        XCTAssertNil(fb.pixelBytes(x: 4, y: 0))
    }

    func testSeveralUpdatesAndAnEmptyUpdate() throws {
        let first = update([rectHeader(0, 0, 1, 1, 0) + px(9)])
        let second = update([])
        let third = update([rectHeader(3, 2, 1, 1, 0) + px(7), rectHeader(0, 1, 1, 1, 0) + px(5)])
        let run = try feedEverySplit(noneHandshake(4, 3) + first + second + third)
        let finished = run.events.compactMap { event -> [RFBRect]? in
            if case .updateFinished(let rects) = event { return rects } else { return nil }
        }
        XCTAssertEqual(finished, [
            [RFBRect(x: 0, y: 0, width: 1, height: 1)],
            [],
            [RFBRect(x: 3, y: 2, width: 1, height: 1), RFBRect(x: 0, y: 1, width: 1, height: 1)],
        ])
        XCTAssertEqual(run.parser.framebuffer.pixelBytes(x: 3, y: 2), px(7))
        XCTAssertEqual(run.parser.framebuffer.pixelBytes(x: 0, y: 1), px(5))
    }

    // MARK: CopyRect

    private func fullScreenRaw(_ w: Int, _ h: Int) -> [UInt8] {
        var pixels: [UInt8] = []
        for i in 0..<(w * h) { pixels += px(i + 1) }
        return update([rectHeader(0, 0, w, h, 0) + pixels])
    }

    func testCopyRect() throws {
        // 4 x 3 screen with pixels 1...12 row by row; copy the 2 x 2 block at (0, 0) to (2, 1).
        let copy = update([rectHeader(2, 1, 2, 2, 1) + be16(0) + be16(0)])
        let run = try feedEverySplit(noneHandshake(4, 3) + fullScreenRaw(4, 3) + copy)
        let fb = run.parser.framebuffer
        XCTAssertEqual(fb.pixelBytes(x: 2, y: 1), px(1))
        XCTAssertEqual(fb.pixelBytes(x: 3, y: 1), px(2))
        XCTAssertEqual(fb.pixelBytes(x: 2, y: 2), px(5))
        XCTAssertEqual(fb.pixelBytes(x: 3, y: 2), px(6))
        XCTAssertEqual(fb.pixelBytes(x: 1, y: 1), px(6)) // untouched
    }

    func testCopyRectWithOverlappingSourceAndDestination() throws {
        // Copy the 3 x 2 block at (0, 0) one pixel to the right: the source must be read before it is overwritten.
        let copy = update([rectHeader(1, 0, 3, 2, 1) + be16(0) + be16(0)])
        let run = try feedEverySplit(noneHandshake(4, 3) + fullScreenRaw(4, 3) + copy)
        let fb = run.parser.framebuffer
        XCTAssertEqual((0..<4).map { fb.pixelBytes(x: $0, y: 0) }, [px(1), px(1), px(2), px(3)])
        XCTAssertEqual((0..<4).map { fb.pixelBytes(x: $0, y: 1) }, [px(5), px(5), px(6), px(7)])
        XCTAssertEqual((0..<4).map { fb.pixelBytes(x: $0, y: 2) }, [px(9), px(10), px(11), px(12)])
    }

    // MARK: Hextile

    private let red: [UInt8] = [0, 0, 255, 0]   // B, G, R, unused
    private let blue: [UInt8] = [255, 0, 0, 0]

    func testHextileBackgroundAndForegroundPersistAcrossTiles() throws {
        // A 20 x 20 rectangle is 2 x 2 tiles: 16 x 16, 4 x 16, 16 x 4 and 4 x 4.
        var tiles: [UInt8] = []
        // Tile 0: background red, foreground blue, one subrect of foreground at (1, 1) sized 2 x 2.
        tiles += cat([14], red, blue, [1, 0x11, 0x11])
        // Tile 1: no flags at all, so it is the persisted red background.
        tiles += [0]
        // Tile 2: one subrect, uncoloured: persisted foreground (blue) at (0, 0), 1 x 1, over the persisted background.
        tiles += [8, 1, 0x00, 0x00]
        // Tile 3: raw, 16 pixels numbered 0...15.
        tiles += cat([1], (0..<16).flatMap { px($0) })
        let stream = noneHandshake(20, 20) + update([rectHeader(0, 0, 20, 20, 5) + tiles])
        let run = try feedEverySplit(stream)
        let fb = run.parser.framebuffer

        XCTAssertEqual(run.events.last, .updateFinished([RFBRect(x: 0, y: 0, width: 20, height: 20)]))
        XCTAssertEqual(fb.pixelBytes(x: 0, y: 0), red)
        XCTAssertEqual(fb.pixelBytes(x: 15, y: 15), red)
        XCTAssertEqual(fb.pixelBytes(x: 1, y: 1), blue)
        XCTAssertEqual(fb.pixelBytes(x: 2, y: 2), blue)
        XCTAssertEqual(fb.pixelBytes(x: 3, y: 3), red)
        XCTAssertEqual(fb.pixelBytes(x: 16, y: 0), red)          // tile 1
        XCTAssertEqual(fb.pixelBytes(x: 19, y: 15), red)
        XCTAssertEqual(fb.pixelBytes(x: 0, y: 16), blue)         // tile 2 subrect with the persisted foreground
        XCTAssertEqual(fb.pixelBytes(x: 1, y: 16), red)
        XCTAssertEqual(fb.pixelBytes(x: 15, y: 19), red)
        XCTAssertEqual(fb.pixelBytes(x: 16, y: 16), px(0))       // tile 3, raw
        XCTAssertEqual(fb.pixelBytes(x: 19, y: 16), px(3))
        XCTAssertEqual(fb.pixelBytes(x: 16, y: 19), px(12))
        XCTAssertEqual(fb.pixelBytes(x: 19, y: 19), px(15))
    }

    func testHextileColouredSubrectsAndRectanglePlacement() throws {
        // A 16 x 16 rectangle placed at (2, 1) on a 20 x 20 screen: one tile with its own colours per subrect.
        let green: [UInt8] = [0, 255, 0, 0]
        var tile = cat([26], red, [2])
        tile += cat(green, [0x00, 0x00])       // 1 x 1 at tile (0, 0)
        tile += cat(blue, [0xFF, 0x00])        // 1 x 1 at tile (15, 15)
        let stream = noneHandshake(20, 20) + update([rectHeader(2, 1, 16, 16, 5) + tile])
        let run = try feedEverySplit(stream)
        let fb = run.parser.framebuffer
        XCTAssertEqual(fb.pixelBytes(x: 2, y: 1), green)
        XCTAssertEqual(fb.pixelBytes(x: 17, y: 16), blue)
        XCTAssertEqual(fb.pixelBytes(x: 3, y: 1), red)
        XCTAssertEqual(fb.pixelBytes(x: 17, y: 15), red)
        XCTAssertEqual(fb.pixelBytes(x: 1, y: 1), px(0))     // outside the rectangle
        XCTAssertEqual(fb.pixelBytes(x: 18, y: 16), px(0))
    }

    func testHextilePersistenceDoesNotLeakBetweenRectangles() throws {
        // The second rectangle's only tile sets nothing: its background is black, not the first one's red.
        let first = cat(rectHeader(0, 0, 4, 4, 5), [2], red)
        let second = cat(rectHeader(0, 0, 4, 4, 5), [0])
        let run = try feedEverySplit(noneHandshake(8, 8) + update([first]) + update([second]))
        XCTAssertEqual(run.parser.framebuffer.pixelBytes(x: 0, y: 0), px(0))
    }

    func testHextileSubrectOutsideItsTileIsRejected() {
        // x = 15, width 2 does not fit a 16-wide tile.
        let tile = cat([10], red, [1, 0xF0, 0x10])
        assertFails(noneHandshake(16, 16) + update([rectHeader(0, 0, 16, 16, 5) + tile]),
                    with: .protocolViolation("a malformed tile"))
        // Edge tile: 4 x 4, a 5 wide subrect is outside it even though a full tile would hold it.
        let edge = cat([10], red, [1, 0x00, 0x40])
        assertFails(noneHandshake(4, 4) + update([rectHeader(0, 0, 4, 4, 5) + edge]),
                    with: .protocolViolation("a malformed tile"))
    }

    // MARK: Limits and malformed input

    func testOversizedScreensAreRejected() {
        assertFails(cat(version, [1, 1], be32(0), serverInit(9000, 10)), with: .tooLarge("The VM's screen is too large to show here (9000 x 10)."))
        // 4097 x 4096 x 4 bytes is just over 64 MiB.
        assertFails(cat(version, [1, 1], be32(0), serverInit(4097, 4096)),
                    with: .tooLarge("The VM's screen is too large to show here (4097 x 4096)."))
        assertFails(cat(version, [1, 1], be32(0), serverInit(0, 600)), with: .protocolViolation("the screen has no size"))
        assertFails(cat(version, [1, 1], be32(0), be16(10), be16(10), pixelFormat, be32(5000)),
                    with: .tooLarge("The screen server sent a desktop name that is too long."))
    }

    func testRectanglesOutsideTheScreenAreRejected() {
        let outside = "an update outside the screen"
        assertFails(noneHandshake(4, 3) + update([rectHeader(3, 0, 2, 1, 0) + px(1) + px(2)]),
                    with: .protocolViolation(outside))
        assertFails(noneHandshake(4, 3) + update([rectHeader(0, 0, 4, 4, 5)]), with: .protocolViolation(outside))
        // CopyRect: destination fits, source does not.
        assertFails(noneHandshake(4, 3) + update([rectHeader(0, 0, 2, 2, 1) + be16(3) + be16(2)]),
                    with: .protocolViolation(outside))
        // A huge claimed raw rectangle is refused up front, not waited for.
        assertFails(noneHandshake(4, 3) + update([rectHeader(0, 0, 65_535, 65_535, 0)]), with: .protocolViolation(outside))
    }

    func testUnknownEncodingsAndMessagesAreRejected() {
        assertFails(noneHandshake(4, 3) + update([rectHeader(0, 0, 1, 1, 16)]),   // ZRLE, never requested
                    with: .protocolViolation("unsupported encoding 16"))
        assertFails(noneHandshake(4, 3) + update([rectHeader(0, 0, 1, 1, -239)]), // Cursor pseudo-encoding
                    with: .protocolViolation("unsupported encoding -239"))
        assertFails(noneHandshake(4, 3) + [9], with: .protocolViolation("unknown message type 9"))
    }

    func testOversizedClipboardAnnouncementIsRejected() {
        assertFails(noneHandshake(4, 3) + [3, 0, 0, 0] + be32(0x1000_0000),
                    with: .tooLarge("The screen server announced a clipboard that is too large."))
    }

    func testBellClipboardAndColourMapAreSkippedWhateverTheChunking() throws {
        let bell: [UInt8] = [2]
        let clipboard: [UInt8] = [3, 0, 0, 0] + be32(5) + Array("hello".utf8)
        let colourMap: [UInt8] = [1, 0] + be16(0) + be16(2) + [UInt8](repeating: 9, count: 12)
        let stream = noneHandshake(4, 3) + bell + clipboard + colourMap + update([])
        let run = try feedEverySplit(stream)
        XCTAssertEqual(Array(run.events.suffix(2)), [.bell, .updateFinished([])])
    }

    // MARK: Resizing

    func testDesktopSizeChange() throws {
        let resize = update([rectHeader(0, 0, 8, 6, -223)])
        let stream = noneHandshake(4, 3) + resize + update([rectHeader(7, 5, 1, 1, 0) + px(3)])
        let run = try feedEverySplit(stream)
        let tail = Array(run.events.suffix(3))
        XCTAssertEqual(tail, [
            .resized(width: 8, height: 6),
            .updateFinished([RFBRect(x: 0, y: 0, width: 8, height: 6)]),
            .updateFinished([RFBRect(x: 7, y: 5, width: 1, height: 1)]),
        ])
        XCTAssertEqual(run.parser.framebuffer.width, 8)
        XCTAssertEqual(run.parser.framebuffer.height, 6)
        XCTAssertEqual(run.parser.framebuffer.pixelBytes(x: 7, y: 5), px(3))
    }

    func testDesktopSizeToTheSameSizeKeepsThePicture() throws {
        let stream = noneHandshake(4, 3) + fullScreenRaw(4, 3) + update([rectHeader(0, 0, 4, 3, -223)])
        let run = try feedEverySplit(stream)
        XCTAssertEqual(run.parser.framebuffer.pixelBytes(x: 3, y: 2), px(12))
        XCTAssertFalse(run.events.contains(.resized(width: 4, height: 3)))
    }

    func testExtendedDesktopSizeChangeAndRefusal() throws {
        let screen = be32(1) + be16(0) + be16(0) + be16(6) + be16(5) + be32(0) // id, x, y, w, h, flags
        let accepted = update([rectHeader(0, 0, 6, 5, -308) + [1, 0, 0, 0] + screen])
        let run = try feedEverySplit(noneHandshake(4, 3) + accepted)
        XCTAssertEqual(run.parser.framebuffer.width, 6)
        XCTAssertEqual(run.parser.framebuffer.height, 5)
        XCTAssertTrue(run.events.contains(.resized(width: 6, height: 5)))

        // Status (the rectangle's y) non-zero: the server refused a resize; nothing changes.
        let refused = update([rectHeader(0, 3, 6, 5, -308) + [1, 0, 0, 0] + screen])
        let kept = try feedEverySplit(noneHandshake(4, 3) + refused)
        XCTAssertEqual(kept.parser.framebuffer.width, 4)
        XCTAssertFalse(kept.events.contains(.resized(width: 6, height: 5)))
    }

    func testOversizedDesktopSizeIsRejected() {
        assertFails(noneHandshake(4, 3) + update([rectHeader(0, 0, 9000, 10, -223)]),
                    with: .tooLarge("The VM's screen is too large to show here (9000 x 10)."))
        assertFails(noneHandshake(4, 3) + update([rectHeader(0, 0, 0, 10, -223)]),
                    with: .protocolViolation("the screen has no size"))
    }

    // MARK: Framebuffer

    func testFramebufferValidation() {
        XCTAssertNoThrow(try RFBFramebuffer(width: 1, height: 1))
        XCTAssertNoThrow(try RFBFramebuffer.validate(width: 4096, height: 4096)) // exactly 64 MiB
        XCTAssertThrowsError(try RFBFramebuffer.validate(width: 4097, height: 4096))
        XCTAssertThrowsError(try RFBFramebuffer.validate(width: 8193, height: 1))
        XCTAssertThrowsError(try RFBFramebuffer.validate(width: 1, height: 0))
        XCTAssertEqual(RFBFramebuffer.empty.width, 0)
    }

    func testFramebufferOperationsIgnoreOutOfRangeRectangles() throws {
        var fb = try RFBFramebuffer(width: 2, height: 2)
        fb.fill(RFBRect(x: 1, y: 1, width: 5, height: 5), pixel: 0xFFFF_FFFF)
        fb.writeRaw(RFBRect(x: -1, y: 0, width: 1, height: 1), from: [1, 2, 3, 4], at: 0)
        fb.copy(from: RFBRect(x: 0, y: 0, width: 3, height: 1), toX: 0, y: 1)
        XCTAssertEqual(fb.pixels, [UInt8](repeating: 0, count: 16))
    }
}

extension Array where Element == UInt8 {
    /// Bytes from a hex string such as "0e86ce".
    init(hex: String) {
        var bytes: [UInt8] = []
        var index = hex.startIndex
        while index < hex.endIndex {
            let next = hex.index(index, offsetBy: 2)
            bytes.append(UInt8(hex[index..<next], radix: 16)!)
            index = next
        }
        self = bytes
    }
}
