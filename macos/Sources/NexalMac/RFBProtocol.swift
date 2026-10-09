import CommonCrypto
import Foundation

// A small, dependency-free RFB (VNC) client protocol, version 3.8 (RFC 6143).
//
// This file is pure: bytes in, bytes and events out. No sockets, no UI, no logging, so it can
// be tested against canned byte streams cut at every offset. RFBConnection.swift owns the
// socket; VMPreviewModel.swift and VMPreviewView.swift own the pictures.
//
// Supported: security None and VNC Authentication (DES challenge); pixel format 32-bit
// true-colour little-endian (B, G, R, unused in memory); encodings Raw, CopyRect, Hextile,
// DesktopSize and ExtendedDesktopSize. Everything else is refused with a plain message.
// The server's password is only ever used to answer the challenge and is never put in an error.

enum RFBLimits {
    /// Largest screen edge accepted.
    static let maxDimension = 8192
    /// Largest framebuffer (width x height x 4) accepted: 64 MiB.
    static let maxFramebufferBytes = 64 * 1024 * 1024
    /// Longest server-sent text (failure reason, desktop name) accepted.
    static let maxTextLength = 4096
    /// Longest clipboard text a server may announce; it is skipped, never stored.
    static let maxCutTextLength = 64 * 1024 * 1024
    /// Unparsed input held at once: one full-size raw rectangle plus slack.
    static let maxBuffered = maxFramebufferBytes + 1024 * 1024
}

enum RFBError: Error, Equatable, LocalizedError {
    case badVersion(String)
    case securityFailed(String)
    case unsupportedSecurity([UInt8])
    case needsPassword
    case authFailed(String)
    case protocolViolation(String)
    case tooLarge(String)

    var errorDescription: String? {
        switch self {
        case .badVersion(let detail):
            return "This is not a screen server this app can talk to (\(detail))."
        case .securityFailed(let reason):
            return "The screen server refused the connection: \(reason)"
        case .unsupportedSecurity(let types):
            let names = types.map { RFBSecurity.name($0) }.joined(separator: ", ")
            return "The screen server needs a sign-in method this viewer does not support (\(names))."
        case .needsPassword:
            return "The screen server needs a password, but none was provided."
        case .authFailed(let reason):
            return "The screen server rejected the password: \(reason)"
        case .protocolViolation(let detail):
            return "The screen server sent something unexpected (\(detail))."
        case .tooLarge(let detail):
            return detail
        }
    }
}

enum RFBSecurity {
    static let none: UInt8 = 1
    static let vncAuthentication: UInt8 = 2

    static func name(_ type: UInt8) -> String {
        switch type {
        case 1: return "None"
        case 2: return "VNC password"
        case 5: return "RA2"
        case 6: return "RA2ne"
        case 16: return "Tight"
        case 18: return "TLS"
        case 19: return "VeNCrypt"
        case 30: return "Apple Remote Desktop"
        case 33: return "MS-Logon"
        default: return "type \(type)"
        }
    }
}

enum RFBEncoding {
    static let raw: Int32 = 0
    static let copyRect: Int32 = 1
    static let hextile: Int32 = 5
    static let desktopSize: Int32 = -223
    static let extendedDesktopSize: Int32 = -308
}

/// Pointer button bits of a PointerEvent.
enum RFBButton {
    static let left: UInt8 = 1
    static let middle: UInt8 = 2
    static let right: UInt8 = 4
    static let wheelUp: UInt8 = 8
    static let wheelDown: UInt8 = 16
    static let wheelLeft: UInt8 = 32
    static let wheelRight: UInt8 = 64
}

enum RFBText {
    /// Server text made safe to show: bounded, without control characters.
    static func sanitize(_ bytes: ArraySlice<UInt8>, limit: Int = 200) -> String {
        let text = String(decoding: bytes.prefix(limit), as: UTF8.self)
        return String(text.unicodeScalars.filter { $0.value >= 0x20 && $0.value != 0x7F })
    }
}

// MARK: - VNC Authentication

enum RFBAuth {
    /// RFB's quirk: each key byte of the DES key has its bits reversed.
    static func reverseBits(_ byte: UInt8) -> UInt8 {
        var value = byte
        var result: UInt8 = 0
        for _ in 0..<8 {
            result = (result << 1) | (value & 1)
            value >>= 1
        }
        return result
    }

    /// The first eight bytes of the password, zero padded, each bit-reversed.
    static func desKey(forPassword password: String) -> [UInt8] {
        var bytes = Array(password.utf8.prefix(8))
        while bytes.count < 8 { bytes.append(0) }
        return bytes.map(reverseBits)
    }

    /// One 8-byte DES-ECB block, encrypted. nil if CommonCrypto refuses.
    static func desEncrypt(block: [UInt8], key: [UInt8]) -> [UInt8]? {
        guard block.count == 8, key.count == 8 else { return nil }
        var out = [UInt8](repeating: 0, count: 16)
        var moved = 0
        let status = CCCrypt(CCOperation(kCCEncrypt), CCAlgorithm(kCCAlgorithmDES), CCOptions(kCCOptionECBMode),
                             key, kCCKeySizeDES, nil, block, block.count, &out, out.count, &moved)
        guard status == CCCryptorStatus(kCCSuccess), moved == 8 else { return nil }
        return Array(out[0..<8])
    }

    /// The 16-byte answer to a 16-byte challenge: each half encrypted with the password key.
    static func response(challenge: [UInt8], password: String) -> [UInt8]? {
        guard challenge.count == 16 else { return nil }
        let keyBytes = desKey(forPassword: password)
        guard let first = desEncrypt(block: Array(challenge[0..<8]), key: keyBytes),
              let second = desEncrypt(block: Array(challenge[8..<16]), key: keyBytes) else { return nil }
        return first + second
    }
}

// MARK: - Client-to-server messages

private extension Array where Element == UInt8 {
    mutating func appendU16(_ value: Int) {
        let v = UInt16(clamping: value)
        append(UInt8(v >> 8)); append(UInt8(v & 0xFF))
    }
    mutating func appendU32(_ value: UInt32) {
        append(UInt8(value >> 24)); append(UInt8((value >> 16) & 0xFF))
        append(UInt8((value >> 8) & 0xFF)); append(UInt8(value & 0xFF))
    }
}

enum RFBEncode {
    /// What this client asks for, most preferred first.
    static let supportedEncodings: [Int32] = [RFBEncoding.hextile, RFBEncoding.copyRect, RFBEncoding.raw,
                                              RFBEncoding.desktopSize, RFBEncoding.extendedDesktopSize]

    /// 32 bits per pixel, depth 24, little-endian, true colour, 8 bits each, red at bit 16 (B, G, R, unused in memory).
    static func setPixelFormat() -> [UInt8] {
        var m: [UInt8] = [0, 0, 0, 0]
        m += [32, 24, 0, 1]
        m.appendU16(255); m.appendU16(255); m.appendU16(255)
        m += [16, 8, 0, 0, 0, 0]
        return m
    }

    static func setEncodings(_ encodings: [Int32]) -> [UInt8] {
        var m: [UInt8] = [2, 0]
        m.appendU16(encodings.count)
        for e in encodings { m.appendU32(UInt32(bitPattern: e)) }
        return m
    }

    static func framebufferUpdateRequest(incremental: Bool, x: Int, y: Int, width: Int, height: Int) -> [UInt8] {
        var m: [UInt8] = [3, incremental ? 1 : 0]
        m.appendU16(x); m.appendU16(y); m.appendU16(width); m.appendU16(height)
        return m
    }

    static func keyEvent(down: Bool, keysym: UInt32) -> [UInt8] {
        var m: [UInt8] = [4, down ? 1 : 0, 0, 0]
        m.appendU32(keysym)
        return m
    }

    static func pointerEvent(buttons: UInt8, x: Int, y: Int) -> [UInt8] {
        var m: [UInt8] = [5, buttons]
        m.appendU16(x); m.appendU16(y)
        return m
    }
}

// MARK: - Framebuffer

struct RFBRect: Equatable {
    var x: Int
    var y: Int
    var width: Int
    var height: Int
}

/// The remote screen: width x height pixels of 4 bytes each, in memory order B, G, R, unused.
struct RFBFramebuffer: Equatable {
    private(set) var width: Int
    private(set) var height: Int
    private(set) var pixels: [UInt8]

    private init() {
        width = 0; height = 0; pixels = []
    }
    static let empty = RFBFramebuffer()

    init(width: Int, height: Int) throws {
        try Self.validate(width: width, height: height)
        self.width = width
        self.height = height
        self.pixels = [UInt8](repeating: 0, count: width * height * 4)
    }

    static func validate(width: Int, height: Int) throws {
        guard width > 0, height > 0 else {
            throw RFBError.protocolViolation("the screen has no size")
        }
        guard width <= RFBLimits.maxDimension, height <= RFBLimits.maxDimension,
              width * height * 4 <= RFBLimits.maxFramebufferBytes else {
            throw RFBError.tooLarge("The VM's screen is too large to show here (\(width) x \(height)).")
        }
    }

    func contains(_ r: RFBRect) -> Bool {
        r.x >= 0 && r.y >= 0 && r.width >= 0 && r.height >= 0
            && r.x + r.width <= width && r.y + r.height <= height
    }

    /// The four bytes of one pixel (B, G, R, unused), or nil outside the screen.
    func pixelBytes(x: Int, y: Int) -> [UInt8]? {
        guard x >= 0, y >= 0, x < width, y < height else { return nil }
        let i = (y * width + x) * 4
        return Array(pixels[i..<i + 4])
    }

    /// `pixel` is four bytes read little-endian: its low byte is the first byte in memory.
    mutating func fill(_ r: RFBRect, pixel: UInt32) {
        guard contains(r), r.width > 0, r.height > 0 else { return }
        let b0 = UInt8(truncatingIfNeeded: pixel), b1 = UInt8(truncatingIfNeeded: pixel >> 8)
        let b2 = UInt8(truncatingIfNeeded: pixel >> 16), b3 = UInt8(truncatingIfNeeded: pixel >> 24)
        let stride = width
        pixels.withUnsafeMutableBufferPointer { p in
            for row in 0..<r.height {
                var i = ((r.y + row) * stride + r.x) * 4
                for _ in 0..<r.width {
                    p[i] = b0; p[i + 1] = b1; p[i + 2] = b2; p[i + 3] = b3
                    i += 4
                }
            }
        }
    }

    /// Copies `r.width * r.height * 4` bytes of raw pixels starting at `offset` in `source`.
    mutating func writeRaw(_ r: RFBRect, from source: [UInt8], at offset: Int) {
        guard contains(r), r.width > 0, r.height > 0 else { return }
        let rowBytes = r.width * 4
        guard offset >= 0, source.count >= offset + rowBytes * r.height else { return }
        for row in 0..<r.height {
            let d = ((r.y + row) * width + r.x) * 4
            let s = offset + row * rowBytes
            pixels.replaceSubrange(d..<d + rowBytes, with: source[s..<s + rowBytes])
        }
    }

    /// CopyRect: the source and destination may overlap, so the source is read first.
    mutating func copy(from src: RFBRect, toX dx: Int, y dy: Int) {
        let dst = RFBRect(x: dx, y: dy, width: src.width, height: src.height)
        guard contains(src), contains(dst), src.width > 0, src.height > 0 else { return }
        let rowBytes = src.width * 4
        var scratch = [UInt8]()
        scratch.reserveCapacity(rowBytes * src.height)
        for row in 0..<src.height {
            let s = ((src.y + row) * width + src.x) * 4
            scratch.append(contentsOf: pixels[s..<s + rowBytes])
        }
        for row in 0..<src.height {
            let d = ((dy + row) * width + dx) * 4
            pixels.replaceSubrange(d..<d + rowBytes, with: scratch[row * rowBytes..<(row + 1) * rowBytes])
        }
    }
}

// MARK: - Server-to-client stream

enum RFBEvent: Equatable {
    /// Bytes the connection must write to the server, in order.
    case send([UInt8])
    /// The session is up: the screen size and the server's name for it.
    case serverInit(width: Int, height: Int, name: String)
    /// A whole FramebufferUpdate has been applied; these rectangles changed. May be empty.
    case updateFinished([RFBRect])
    /// The server changed the screen size. The framebuffer was reallocated (blank).
    case resized(width: Int, height: Int)
    case bell
}

/// Streaming parser for the server side of an RFB session. Feed it whatever bytes arrive, in
/// any chunking; it returns the events that became complete. After a thrown error it is dead.
struct RFBParser {
    private enum Phase: Equatable {
        case version, securityList, challenge, securityResult, serverInit, messages, update, dead
    }

    private struct HextileProgress {
        var rect: RFBRect
        var tile = 0
        var background: UInt32 = 0
        var foreground: UInt32 = 0
    }

    private var phase: Phase = .version
    private var buf: [UInt8] = []
    private var pos = 0
    private var skipRemaining = 0
    private var remainingRects = 0
    private var dirty: [RFBRect] = []
    private var hextile: HextileProgress?
    private let password: String?

    private(set) var framebuffer = RFBFramebuffer.empty
    private(set) var desktopName = ""

    /// True once ServerInit has been received.
    var isLive: Bool { phase == .messages || phase == .update }

    init(password: String?) {
        self.password = (password?.isEmpty == false) ? password : nil
    }

    mutating func feed(_ bytes: [UInt8]) throws -> [RFBEvent] {
        if phase == .dead { throw RFBError.protocolViolation("the connection already failed") }
        buf.append(contentsOf: bytes)
        if buf.count - pos > RFBLimits.maxBuffered {
            phase = .dead
            throw RFBError.tooLarge("The screen server sent more data than this viewer will hold.")
        }
        var events: [RFBEvent] = []
        do {
            while try step(&events) {}
        } catch {
            phase = .dead
            throw error
        }
        compact()
        return events
    }

    // MARK: Buffer helpers

    private var available: Int { buf.count - pos }
    private func u8(_ offset: Int) -> Int { Int(buf[pos + offset]) }
    private func u16(_ offset: Int) -> Int {
        let high = Int(buf[pos + offset]) << 8
        return high | Int(buf[pos + offset + 1])
    }
    private func u32(_ offset: Int) -> UInt32 {
        var value: UInt32 = 0
        for i in 0..<4 { value = (value << 8) | UInt32(buf[pos + offset + i]) }
        return value
    }
    /// A pixel as it sits in the stream (four bytes, first byte lowest).
    private func pixel(at index: Int) -> UInt32 {
        var value: UInt32 = 0
        for i in (0..<4).reversed() { value = (value << 8) | UInt32(buf[index + i]) }
        return value
    }

    private mutating func compact() {
        if pos == buf.count {
            buf.removeAll(keepingCapacity: true)
            pos = 0
        } else if pos > 65_536 {
            buf.removeSubrange(0..<pos)
            pos = 0
        }
    }

    // MARK: State machine

    /// Advances one step. true: progress was made, call again. false: more bytes are needed.
    private mutating func step(_ events: inout [RFBEvent]) throws -> Bool {
        if skipRemaining > 0 {
            let n = min(skipRemaining, available)
            pos += n
            skipRemaining -= n
            return skipRemaining == 0
        }
        switch phase {
        case .version: return try stepVersion(&events)
        case .securityList: return try stepSecurityList(&events)
        case .challenge: return try stepChallenge(&events)
        case .securityResult: return try stepSecurityResult(&events)
        case .serverInit: return try stepServerInit(&events)
        case .messages: return try stepMessage(&events)
        case .update: return try stepUpdate(&events)
        case .dead: return false
        }
    }

    private mutating func stepVersion(_ events: inout [RFBEvent]) throws -> Bool {
        guard available >= 12 else { return false }
        let line = Array(buf[pos..<pos + 12])
        func digit(_ b: UInt8) -> Bool { b >= 48 && b <= 57 }
        guard Array(line[0..<4]) == Array("RFB ".utf8), line[7] == 46, line[11] == 10,
              line[4..<7].allSatisfy(digit), line[8..<11].allSatisfy(digit) else {
            throw RFBError.badVersion("it did not identify itself as VNC")
        }
        let major = Int(String(decoding: line[4..<7], as: UTF8.self)) ?? 0
        let minor = Int(String(decoding: line[8..<11], as: UTF8.self)) ?? 0
        guard major == 3, minor >= 8 else {
            throw RFBError.badVersion("it speaks RFB \(major).\(minor), and 3.8 or newer is required")
        }
        pos += 12
        events.append(.send(Array("RFB 003.008\n".utf8)))
        phase = .securityList
        return true
    }

    private mutating func stepSecurityList(_ events: inout [RFBEvent]) throws -> Bool {
        guard available >= 1 else { return false }
        let count = u8(0)
        if count == 0 {
            guard available >= 5 else { return false }
            let length = Int(u32(1))
            guard length <= RFBLimits.maxTextLength else {
                throw RFBError.securityFailed("no reason given")
            }
            guard available >= 5 + length else { return false }
            let reason = RFBText.sanitize(buf[pos + 5..<pos + 5 + length])
            throw RFBError.securityFailed(reason.isEmpty ? "no reason given" : reason)
        }
        guard available >= 1 + count else { return false }
        let types = Array(buf[pos + 1..<pos + 1 + count])
        pos += 1 + count
        let chosen: UInt8
        if types.contains(RFBSecurity.vncAuthentication), password != nil {
            chosen = RFBSecurity.vncAuthentication
        } else if types.contains(RFBSecurity.none) {
            chosen = RFBSecurity.none
        } else if types.contains(RFBSecurity.vncAuthentication) {
            throw RFBError.needsPassword
        } else {
            throw RFBError.unsupportedSecurity(types)
        }
        events.append(.send([chosen]))
        phase = chosen == RFBSecurity.vncAuthentication ? .challenge : .securityResult
        return true
    }

    private mutating func stepChallenge(_ events: inout [RFBEvent]) throws -> Bool {
        guard available >= 16 else { return false }
        let challenge = Array(buf[pos..<pos + 16])
        guard let password, let answer = RFBAuth.response(challenge: challenge, password: password) else {
            throw RFBError.protocolViolation("the password could not be applied")
        }
        pos += 16
        events.append(.send(answer))
        phase = .securityResult
        return true
    }

    private mutating func stepSecurityResult(_ events: inout [RFBEvent]) throws -> Bool {
        guard available >= 4 else { return false }
        if u32(0) == 0 {
            pos += 4
            events.append(.send([1])) // ClientInit: share the screen, do not disconnect other viewers.
            phase = .serverInit
            return true
        }
        // Failed: RFB 3.8 follows the result with a reason string.
        guard available >= 8 else { return false }
        let length = Int(u32(4))
        guard length <= RFBLimits.maxTextLength else { throw RFBError.authFailed("no reason given") }
        guard available >= 8 + length else { return false }
        let reason = RFBText.sanitize(buf[pos + 8..<pos + 8 + length])
        throw RFBError.authFailed(reason.isEmpty ? "authentication failed" : reason)
    }

    private mutating func stepServerInit(_ events: inout [RFBEvent]) throws -> Bool {
        guard available >= 24 else { return false }
        let w = u16(0), h = u16(2)
        let nameLength = Int(u32(20))
        guard nameLength <= RFBLimits.maxTextLength else {
            throw RFBError.tooLarge("The screen server sent a desktop name that is too long.")
        }
        guard available >= 24 + nameLength else { return false }
        framebuffer = try RFBFramebuffer(width: w, height: h)
        desktopName = RFBText.sanitize(buf[pos + 24..<pos + 24 + nameLength])
        pos += 24 + nameLength
        events.append(.serverInit(width: w, height: h, name: desktopName))
        var out = RFBEncode.setPixelFormat()
        out += RFBEncode.setEncodings(RFBEncode.supportedEncodings)
        out += RFBEncode.framebufferUpdateRequest(incremental: false, x: 0, y: 0, width: w, height: h)
        events.append(.send(out))
        phase = .messages
        return true
    }

    private mutating func stepMessage(_ events: inout [RFBEvent]) throws -> Bool {
        guard available >= 1 else { return false }
        switch u8(0) {
        case 0: // FramebufferUpdate
            guard available >= 4 else { return false }
            remainingRects = u16(2)
            pos += 4
            dirty = []
            phase = .update
            return true
        case 1: // SetColourMapEntries: never used with true colour; skipped.
            guard available >= 6 else { return false }
            let entries = u16(4)
            pos += 6
            skipRemaining = entries * 6
            return true
        case 2: // Bell
            pos += 1
            events.append(.bell)
            return true
        case 3: // ServerCutText: skipped, never stored.
            guard available >= 8 else { return false }
            let length = Int(u32(4))
            guard length <= RFBLimits.maxCutTextLength else {
                throw RFBError.tooLarge("The screen server announced a clipboard that is too large.")
            }
            pos += 8
            skipRemaining = length
            return true
        default:
            throw RFBError.protocolViolation("unknown message type \(u8(0))")
        }
    }

    private mutating func stepUpdate(_ events: inout [RFBEvent]) throws -> Bool {
        if hextile != nil { return try stepHextile() }
        if remainingRects == 0 {
            events.append(.updateFinished(dirty))
            dirty = []
            phase = .messages
            return true
        }
        guard available >= 12 else { return false }
        let rect = RFBRect(x: u16(0), y: u16(2), width: u16(4), height: u16(6))
        let encoding = Int32(bitPattern: u32(8))
        switch encoding {
        case RFBEncoding.raw:
            try requireInside(rect)
            let need = rect.width * rect.height * 4
            guard available >= 12 + need else { return false }
            let source = buf
            framebuffer.writeRaw(rect, from: source, at: pos + 12)
            pos += 12 + need
            finishRect(rect)
            return true
        case RFBEncoding.copyRect:
            guard available >= 16 else { return false }
            try requireInside(rect)
            let src = RFBRect(x: u16(12), y: u16(14), width: rect.width, height: rect.height)
            try requireInside(src)
            framebuffer.copy(from: src, toX: rect.x, y: rect.y)
            pos += 16
            finishRect(rect)
            return true
        case RFBEncoding.hextile:
            try requireInside(rect)
            pos += 12
            hextile = HextileProgress(rect: rect)
            return true
        case RFBEncoding.desktopSize:
            try RFBFramebuffer.validate(width: rect.width, height: rect.height)
            pos += 12
            try resize(width: rect.width, height: rect.height, events: &events)
            remainingRects -= 1
            return true
        case RFBEncoding.extendedDesktopSize:
            guard available >= 16 else { return false }
            let screens = u8(12)
            let total = 16 + screens * 16
            guard available >= total else { return false }
            // rect.y is the status: non-zero means the server refused a resize, so nothing changed.
            let accepted = rect.y == 0
            if accepted { try RFBFramebuffer.validate(width: rect.width, height: rect.height) }
            pos += total
            if accepted { try resize(width: rect.width, height: rect.height, events: &events) }
            remainingRects -= 1
            return true
        default:
            throw RFBError.protocolViolation("unsupported encoding \(encoding)")
        }
    }

    private mutating func finishRect(_ rect: RFBRect) {
        dirty.append(rect)
        remainingRects -= 1
    }

    private func requireInside(_ rect: RFBRect) throws {
        guard framebuffer.contains(rect) else {
            throw RFBError.protocolViolation("an update outside the screen")
        }
    }

    private mutating func resize(width: Int, height: Int, events: inout [RFBEvent]) throws {
        guard width != framebuffer.width || height != framebuffer.height else { return }
        framebuffer = try RFBFramebuffer(width: width, height: height)
        events.append(.resized(width: width, height: height))
        dirty.append(RFBRect(x: 0, y: 0, width: width, height: height))
    }

    // MARK: Hextile

    /// Continues the current Hextile rectangle, tile by tile. A tile is only decoded once all of
    /// its bytes have arrived, so a stream cut anywhere resumes at a tile boundary.
    private mutating func stepHextile() throws -> Bool {
        guard var hx = hextile else { return true }
        let rect = hx.rect
        let tilesX = (rect.width + 15) / 16
        let tilesY = (rect.height + 15) / 16
        let total = tilesX * tilesY
        while hx.tile < total {
            let tx = hx.tile % tilesX, ty = hx.tile / tilesX
            let tw = min(16, rect.width - tx * 16), th = min(16, rect.height - ty * 16)
            guard let need = hextileTileLength(tw: tw, th: th), available >= need else {
                hextile = hx
                return false
            }
            try decodeHextileTile(&hx, tx: tx, ty: ty, tw: tw, th: th)
            pos += need
            hx.tile += 1
        }
        hextile = nil
        finishRect(rect)
        return true
    }

    /// Byte length of the tile at `pos`, or nil while its header is not complete.
    private func hextileTileLength(tw: Int, th: Int) -> Int? {
        guard available >= 1 else { return nil }
        let sub = u8(0)
        if sub & 1 != 0 { return 1 + tw * th * 4 }
        var length = 1
        if sub & 2 != 0 { length += 4 }
        if sub & 4 != 0 { length += 4 }
        if sub & 8 != 0 {
            guard available > length else { return nil }
            let count = u8(length)
            length += 1 + count * (sub & 16 != 0 ? 6 : 2)
        }
        return length
    }

    private mutating func decodeHextileTile(_ hx: inout HextileProgress, tx: Int, ty: Int, tw: Int, th: Int) throws {
        var q = pos
        let sub = Int(buf[q]); q += 1
        let tile = RFBRect(x: hx.rect.x + tx * 16, y: hx.rect.y + ty * 16, width: tw, height: th)
        if sub & 1 != 0 {
            let source = buf
            framebuffer.writeRaw(tile, from: source, at: q)
            return
        }
        if sub & 2 != 0 { hx.background = pixel(at: q); q += 4 }
        if sub & 4 != 0 { hx.foreground = pixel(at: q); q += 4 }
        framebuffer.fill(tile, pixel: hx.background)
        guard sub & 8 != 0 else { return }
        let count = Int(buf[q]); q += 1
        let coloured = sub & 16 != 0
        for _ in 0..<count {
            var colour = hx.foreground
            if coloured { colour = pixel(at: q); q += 4 }
            let xy = Int(buf[q]), wh = Int(buf[q + 1]); q += 2
            let sx = xy >> 4, sy = xy & 15, sw = (wh >> 4) + 1, sh = (wh & 15) + 1
            guard sx + sw <= tw, sy + sh <= th else {
                throw RFBError.protocolViolation("a malformed tile")
            }
            framebuffer.fill(RFBRect(x: tile.x + sx, y: tile.y + sy, width: sw, height: sh), pixel: colour)
        }
    }
}

// MARK: - Keys

/// macOS virtual key codes and characters to X11 keysyms. Pure tables, so they can be tested.
enum RFBKeyMap {
    /// Keys whose meaning does not depend on the typed character.
    static func specialKeysym(forKeyCode code: UInt16) -> UInt32? {
        switch code {
        case 0x24: return 0xFF0D // Return
        case 0x4C: return 0xFF8D // keypad Enter
        case 0x30: return 0xFF09 // Tab
        case 0x31: return 0x0020 // Space
        case 0x33: return 0xFF08 // Backspace
        case 0x35: return 0xFF1B // Escape
        case 0x75: return 0xFFFF // Forward delete
        case 0x72: return 0xFF63 // Insert (Help)
        case 0x73: return 0xFF50 // Home
        case 0x77: return 0xFF57 // End
        case 0x74: return 0xFF55 // Page up
        case 0x79: return 0xFF56 // Page down
        case 0x7B: return 0xFF51 // Left
        case 0x7E: return 0xFF52 // Up
        case 0x7C: return 0xFF53 // Right
        case 0x7D: return 0xFF54 // Down
        case 0x7A: return 0xFFBE // F1
        case 0x78: return 0xFFBF // F2
        case 0x63: return 0xFFC0 // F3
        case 0x76: return 0xFFC1 // F4
        case 0x60: return 0xFFC2 // F5
        case 0x61: return 0xFFC3 // F6
        case 0x62: return 0xFFC4 // F7
        case 0x64: return 0xFFC5 // F8
        case 0x65: return 0xFFC6 // F9
        case 0x6D: return 0xFFC7 // F10
        case 0x67: return 0xFFC8 // F11
        case 0x6F: return 0xFFC9 // F12
        default: return nil
        }
    }

    /// Modifier keys, which arrive as flagsChanged events. Caps Lock is not forwarded.
    static func modifierKeysym(forKeyCode code: UInt16) -> UInt32? {
        switch code {
        case 0x38: return 0xFFE1 // Shift_L
        case 0x3C: return 0xFFE2 // Shift_R
        case 0x3B: return 0xFFE3 // Control_L
        case 0x3E: return 0xFFE4 // Control_R
        case 0x3A: return 0xFFE9 // Option -> Alt_L
        case 0x3D: return 0xFFEA // Right Option -> Alt_R
        case 0x37: return 0xFFEB // Command -> Super_L
        case 0x36: return 0xFFEC // Right Command -> Super_R
        default: return nil
        }
    }

    /// The keysym of a typed character: Latin-1 as is, other Unicode as 0x01000000 + code point.
    /// nil for control characters and for the private-use codes macOS uses for function keys.
    static func keysym(forScalar value: UInt32) -> UInt32? {
        switch value {
        case 0x20...0x7E, 0xA0...0xFF: return value
        case 0..<0x20, 0x7F...0x9F: return nil
        case 0xF700...0xF8FF: return nil
        case 0x110000...: return nil
        default: return 0x0100_0000 | value
        }
    }
}
