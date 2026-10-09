import AppKit
import SwiftUI

// Built-in live view of a virtual machine's screen (hover thumbnail and live window).
// This file: the pure pieces (what may be previewed, geometry, the credentials value) and
// the model that connects, keeps the latest picture and forwards input.
// The views are in VMPreviewView.swift; the protocol is in RFBProtocol.swift / RFBConnection.swift.

/// Where a VM's VNC server is, and its password. The password stays in memory and never
/// appears in descriptions, so it cannot reach a log line by interpolation.
struct VNCEndpoint: Equatable, CustomStringConvertible, CustomDebugStringConvertible {
    let host: String
    let port: Int
    let password: String

    var description: String { "vnc://\(host):\(port) (password hidden)" }
    var debugDescription: String { description }
}

/// What the row offers a VM that is in a given state. Only a running virtual machine has a screen.
enum VMPreviewEligibility {
    /// nil when the VM can be previewed, else the plain reason it cannot.
    static func unavailableReason(kind: String?, state: String?, paused: Bool? = nil, desktop: Bool? = nil) -> String? {
        if kind == "devcontainer" { return "Development containers have no screen." }
        if desktop == false { return "Created without a desktop." }
        if paused == true || state == "paused" { return "Paused while this Mac sleeps." }
        switch state ?? "" {
        case "running": return nil
        case "stopped": return "Stopped. Start it to see its screen."
        case "provisioning", "requested": return "Still starting."
        case "stopping": return "Shutting down."
        case "failed": return "Failed to start."
        default: return "Not running."
        }
    }
}

/// Everything the row hands to the preview and the live window.
struct VMPreviewTarget {
    /// The coordinator's id of the host.
    let id: String
    let title: String
    /// Asks the connector for the screen's address and a fresh password (every call may mint a new one).
    let fetchEndpoint: @MainActor () async throws -> VNCEndpoint
    /// The existing hand-off to macOS Screen Sharing.
    let openScreenSharing: @MainActor () -> Void
}

/// Fit-to-view geometry shared by drawing and by mapping mouse positions. View coordinates
/// have their origin at the bottom left (AppKit's default); framebuffer coordinates at the top left.
enum VMScreenGeometry {
    /// Where the picture is drawn: as large as fits, centred, aspect ratio kept.
    static func fitRect(content: CGSize, in bounds: CGSize) -> CGRect {
        guard content.width > 0, content.height > 0, bounds.width > 0, bounds.height > 0 else { return .zero }
        let scale = min(bounds.width / content.width, bounds.height / content.height)
        let w = content.width * scale, h = content.height * scale
        return CGRect(x: (bounds.width - w) / 2, y: (bounds.height - h) / 2, width: w, height: h)
    }

    /// The remote pixel under a view point, clamped to the screen so a drag that leaves the
    /// picture still ends on its edge. nil when there is no picture yet.
    static func framebufferPoint(forViewPoint p: CGPoint, viewSize: CGSize, framebuffer: CGSize) -> (x: Int, y: Int)? {
        let fit = fitRect(content: framebuffer, in: viewSize)
        guard fit.width > 0, fit.height > 0 else { return nil }
        let fx = (p.x - fit.minX) / fit.width * framebuffer.width
        let fy = (fit.maxY - p.y) / fit.height * framebuffer.height
        let x = min(max(Int(fx.rounded(.down)), 0), Int(framebuffer.width) - 1)
        let y = min(max(Int(fy.rounded(.down)), 0), Int(framebuffer.height) - 1)
        return (x, y)
    }
}

@MainActor
final class VMPreviewModel: ObservableObject {
    enum Status: Equatable {
        case idle
        case connecting
        case live
        /// Could not connect, or the connection broke. The text is shown after "Preview unavailable:".
        case unavailable(String)
        /// Ended on purpose or by the preview's time limit.
        case closed
    }

    @Published private(set) var status: Status = .idle
    @Published private(set) var frame: CGImage?
    /// Remote screen size in pixels, zero until the first frame.
    @Published private(set) var framebufferSize: CGSize = .zero
    /// On by default: nothing typed or clicked here reaches the VM until this is turned off.
    @Published var viewOnly = true

    let mode: RFBConnection.Mode
    private let fetchEndpoint: @MainActor () async throws -> VNCEndpoint
    private var connection: RFBConnection?
    private var startTask: Task<Void, Never>?
    private var generation = 0

    /// The connector says "pending" while the guest is still setting the password: wait and ask again.
    private static let pendingAttempts = 5
    private static let pendingDelay: UInt64 = 2_000_000_000

    init(mode: RFBConnection.Mode, fetchEndpoint: @escaping @MainActor () async throws -> VNCEndpoint) {
        self.mode = mode
        self.fetchEndpoint = fetchEndpoint
    }

    /// Fetches the address and password, then connects. Does nothing while already running.
    func start() {
        guard startTask == nil, connection == nil else { return }
        generation += 1
        let mine = generation
        status = .connecting
        frame = nil
        framebufferSize = .zero
        startTask = Task { [weak self] in
            guard let self else { return }
            do {
                let endpoint = try await self.fetchWithRetry()
                guard !Task.isCancelled, self.generation == mine else { return }
                self.connect(to: endpoint)
            } catch {
                guard !Task.isCancelled, self.generation == mine else { return }
                self.startTask = nil
                self.status = .unavailable(error.localizedDescription)
            }
        }
    }

    /// Ends everything and releases the picture. Safe to call repeatedly.
    func stop() {
        generation += 1
        startTask?.cancel()
        startTask = nil
        connection?.stop()
        connection = nil
        frame = nil
        framebufferSize = .zero
        status = .idle
    }

    /// For the live window's Reconnect button: a fresh password and a new connection.
    func restart() {
        stop()
        start()
    }

    private func fetchWithRetry() async throws -> VNCEndpoint {
        var attempt = 1
        while true {
            do {
                return try await fetchEndpoint()
            } catch ThrowawayConnectError.pending where attempt < Self.pendingAttempts {
                attempt += 1
                try await Task.sleep(nanoseconds: Self.pendingDelay)
                try Task.checkCancellation()
            }
        }
    }

    private func connect(to endpoint: VNCEndpoint) {
        let conn = RFBConnection(host: endpoint.host, port: endpoint.port, password: endpoint.password, mode: mode)
        conn.onState = { [weak self] state in self?.handle(state) }
        conn.onFrame = { [weak self] frame in self?.handle(frame) }
        connection = conn
        conn.start()
    }

    private func handle(_ state: RFBConnection.State) {
        switch state {
        case .connecting, .authenticating:
            status = .connecting
        case .live:
            status = .live
        case .failed(let reason):
            connection?.stop()
            connection = nil
            startTask = nil
            status = .unavailable(reason)
        case .closed:
            connection = nil
            startTask = nil
            status = .closed
        }
    }

    private func handle(_ frame: RFBConnection.Frame) {
        self.frame = frame.image
        framebufferSize = CGSize(width: frame.width, height: frame.height)
    }

    // MARK: Input (the window's view calls these only while view-only is off)

    func sendKey(keysym: UInt32, down: Bool) {
        guard status == .live, down == false || !viewOnly else { return }
        connection?.sendKey(keysym: keysym, down: down)
    }

    func sendPointer(buttons: UInt8, x: Int, y: Int) {
        guard status == .live, buttons == 0 || !viewOnly else { return }
        connection?.sendPointer(buttons: buttons, x: x, y: y)
    }
}
