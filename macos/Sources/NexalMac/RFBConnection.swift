import CoreGraphics
import Foundation
import Network

// The transport half of the built-in VM screen viewer: one TCP connection to a VM's VNC server,
// driven through RFBParser (RFBProtocol.swift).
//
// All state lives on a private serial queue. Callbacks (`onState`, `onFrame`) are delivered on the
// main actor. A connection is used once: it never reconnects; the owner makes a new one.
// Nothing here logs. The password is held only in memory and dropped when the connection ends.

final class RFBConnection: @unchecked Sendable {
    enum State: Equatable {
        case connecting
        case authenticating
        case live
        case failed(String)
        case closed
    }

    enum Mode {
        /// A small hover thumbnail: at most 2 updates per second, ends by itself after 2 minutes.
        case preview
        /// A live window: updates are requested as fast as they arrive (capped near 60 per second).
        case window

        var minRequestInterval: TimeInterval { self == .preview ? 0.5 : 1.0 / 60.0 }
        var maxSession: TimeInterval? { self == .preview ? 120 : nil }
    }

    struct Frame {
        let image: CGImage
        let width: Int
        let height: Int
    }

    /// From the first byte of the connect attempt until the screen is up.
    static let handshakeTimeout: TimeInterval = 15
    /// No bytes at all from the server for this long ends the connection.
    static let idleTimeout: TimeInterval = 30
    /// A still screen sends nothing, so after this much silence a full refresh is requested.
    static let keepAliveInterval: TimeInterval = 10

    /// Set before `start()`, and cleared by `stop()`; touched on the main thread only.
    var onState: (@MainActor (State) -> Void)?
    var onFrame: (@MainActor (Frame) -> Void)?

    private let host: String
    private let port: Int
    private let mode: Mode
    private let queue = DispatchQueue(label: "nexal.rfb.connection")

    // Everything below is confined to `queue`.
    private var parser: RFBParser
    private var connection: NWConnection?
    private var timer: DispatchSourceTimer?
    private var state: State = .connecting
    private var started = false
    private var finished = false
    private var startedAt = Date()
    private var lastReceive = Date()
    private var lastRequestAt = Date.distantPast
    private var requestOutstanding = false
    private var requestScheduled = false
    private var needFullRequest = false

    private let frameLock = NSLock()
    private var pendingFrame: Frame?

    init(host: String, port: Int, password: String?, mode: Mode) {
        self.host = host
        self.port = port
        self.mode = mode
        self.parser = RFBParser(password: password)
    }

    deinit {
        timer?.cancel()
        connection?.cancel()
    }

    // MARK: Public (call from the main thread)

    func start() {
        queue.async { self.begin() }
    }

    /// Ends the connection and silences the callbacks. Safe to call any number of times.
    func stop() {
        onState = nil
        onFrame = nil
        queue.async { self.finish(.closed) }
    }

    func sendKey(keysym: UInt32, down: Bool) {
        queue.async {
            guard self.state == .live else { return }
            self.send(RFBEncode.keyEvent(down: down, keysym: keysym))
        }
    }

    func sendPointer(buttons: UInt8, x: Int, y: Int) {
        queue.async {
            guard self.state == .live else { return }
            self.send(RFBEncode.pointerEvent(buttons: buttons, x: x, y: y))
        }
    }

    // MARK: Lifecycle (on queue)

    private func begin() {
        guard !started, !finished else { return }
        started = true
        startedAt = Date()
        lastReceive = Date()
        guard (1...65535).contains(port), !host.isEmpty, let nwPort = NWEndpoint.Port(rawValue: UInt16(port)) else {
            fail("The VM's screen address is not valid.")
            return
        }
        let tcp = NWProtocolTCP.Options()
        tcp.noDelay = true
        tcp.connectionTimeout = Int(Self.handshakeTimeout)
        let conn = NWConnection(host: NWEndpoint.Host(host), port: nwPort, using: NWParameters(tls: nil, tcp: tcp))
        connection = conn
        conn.stateUpdateHandler = { [weak self] newState in
            self?.connectionStateChanged(newState)
        }
        conn.start(queue: queue)
        startTimer()
    }

    private func connectionStateChanged(_ newState: NWConnection.State) {
        guard !finished else { return }
        switch newState {
        case .ready:
            publish(.authenticating)
            receive()
        case .failed(let error):
            fail(Self.describe(error))
        case .waiting(let error):
            // No route or refused: do not sit in "waiting"; report it so the owner can retry.
            fail(Self.describe(error))
        default:
            break
        }
    }

    private func startTimer() {
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now() + 2, repeating: 2)
        t.setEventHandler { [weak self] in self?.tick() }
        t.resume()
        timer = t
    }

    private func tick() {
        guard !finished else { return }
        let now = Date()
        if state != .live, now.timeIntervalSince(startedAt) > Self.handshakeTimeout {
            fail("The VM's screen server did not answer in time.")
            return
        }
        if let maxSession = mode.maxSession, now.timeIntervalSince(startedAt) > maxSession {
            finish(.closed)
            return
        }
        let silence = now.timeIntervalSince(lastReceive)
        if silence > Self.idleTimeout {
            fail("The VM stopped responding.")
            return
        }
        if state == .live, silence > Self.keepAliveInterval, now.timeIntervalSince(lastRequestAt) > Self.keepAliveInterval {
            sendUpdateRequest(incremental: false)
        }
    }

    private func fail(_ reason: String) {
        finish(.failed(reason))
    }

    private func finish(_ terminal: State) {
        guard !finished else { return }
        finished = true
        timer?.cancel()
        timer = nil
        connection?.stateUpdateHandler = nil
        connection?.cancel()
        connection = nil
        // Drop the buffered pixels and the password the parser holds.
        parser = RFBParser(password: nil)
        publish(terminal)
    }

    // MARK: Receiving

    private func receive() {
        guard let conn = connection, !finished else { return }
        conn.receive(minimumIncompleteLength: 1, maximumLength: 256 * 1024) { [weak self] data, _, isComplete, error in
            guard let self, !self.finished else { return }
            if let data, !data.isEmpty {
                self.lastReceive = Date()
                self.consume(data)
            }
            if self.finished { return }
            if let error {
                self.fail(Self.describe(error))
                return
            }
            if isComplete {
                self.serverClosed()
                return
            }
            self.receive()
        }
    }

    private func serverClosed() {
        if parser.isLive {
            fail("The VM closed the screen connection.")
        } else {
            fail("The VM's screen server closed the connection during sign-in. The password may have been rejected.")
        }
    }

    private func consume(_ data: Data) {
        let events: [RFBEvent]
        do {
            events = try parser.feed(Array(data))
        } catch {
            fail(error.localizedDescription)
            return
        }
        for event in events {
            handle(event)
            if finished { return }
        }
    }

    private func handle(_ event: RFBEvent) {
        switch event {
        case .send(let bytes):
            send(bytes)
        case .serverInit:
            // The first full-screen request went out together with the pixel format.
            requestOutstanding = true
            lastRequestAt = Date()
            publish(.live)
        case .resized:
            needFullRequest = true
        case .updateFinished(let dirty):
            requestOutstanding = false
            if !dirty.isEmpty { emitFrame() }
            scheduleRequest()
        case .bell:
            break
        }
    }

    // MARK: Sending

    private func send(_ bytes: [UInt8]) {
        guard let conn = connection, !finished else { return }
        conn.send(content: Data(bytes), completion: .contentProcessed { [weak self] error in
            if let error { self?.fail(Self.describe(error)) }
        })
    }

    /// At most one request is in flight, and requests are spaced by the mode's minimum interval.
    private func scheduleRequest() {
        guard !finished, !requestOutstanding, !requestScheduled else { return }
        let wait = max(0, mode.minRequestInterval - Date().timeIntervalSince(lastRequestAt))
        requestScheduled = true
        queue.asyncAfter(deadline: .now() + wait) { [weak self] in
            guard let self, !self.finished else { return }
            self.requestScheduled = false
            self.sendUpdateRequest(incremental: !self.needFullRequest)
        }
    }

    private func sendUpdateRequest(incremental: Bool) {
        let fb = parser.framebuffer
        guard fb.width > 0, fb.height > 0 else { return }
        needFullRequest = false
        requestOutstanding = true
        lastRequestAt = Date()
        send(RFBEncode.framebufferUpdateRequest(incremental: incremental, x: 0, y: 0, width: fb.width, height: fb.height))
    }

    // MARK: Publishing

    private func publish(_ new: State) {
        guard new != state else { return }
        state = new
        DispatchQueue.main.async { [weak self] in
            MainActor.assumeIsolated {
                guard let self else { return }
                self.onState?(new)
            }
        }
    }

    private func emitFrame() {
        let fb = parser.framebuffer
        guard let image = fb.makeImage() else { return }
        let frame = Frame(image: image, width: fb.width, height: fb.height)
        frameLock.lock()
        let schedule = pendingFrame == nil
        pendingFrame = frame
        frameLock.unlock()
        // Frames that pile up while the main thread is busy are replaced, not queued.
        guard schedule else { return }
        DispatchQueue.main.async { [weak self] in
            MainActor.assumeIsolated {
                guard let self else { return }
                self.frameLock.lock()
                let latest = self.pendingFrame
                self.pendingFrame = nil
                self.frameLock.unlock()
                if let latest { self.onFrame?(latest) }
            }
        }
    }

    static func describe(_ error: NWError) -> String {
        if case .posix(let code) = error {
            switch code {
            case .ECONNREFUSED:
                return "The VM's screen server refused the connection. It may still be starting."
            case .ETIMEDOUT, .EHOSTUNREACH, .ENETUNREACH, .EHOSTDOWN, .ENETDOWN:
                return "The VM could not be reached."
            case .ECONNRESET, .EPIPE:
                return "The connection to the VM was lost."
            default:
                break
            }
        }
        return "Network error: \(error)"
    }
}

extension RFBFramebuffer {
    /// A CGImage of the current pixels (a copy, so later updates do not change it).
    func makeImage() -> CGImage? {
        guard width > 0, height > 0, let provider = CGDataProvider(data: Data(pixels) as CFData) else { return nil }
        let space = CGColorSpace(name: CGColorSpace.sRGB) ?? CGColorSpaceCreateDeviceRGB()
        let info = CGBitmapInfo(rawValue: CGBitmapInfo.byteOrder32Little.rawValue | CGImageAlphaInfo.noneSkipFirst.rawValue)
        return CGImage(width: width, height: height, bitsPerComponent: 8, bitsPerPixel: 32, bytesPerRow: width * 4,
                       space: space, bitmapInfo: info, provider: provider, decode: nil,
                       shouldInterpolate: true, intent: .defaultIntent)
    }
}
