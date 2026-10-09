import AppKit
import SwiftUI

// Views for the built-in VM screen viewer: the hover thumbnail and the live window.
// The model is VMPreviewModel.swift; the protocol is RFBProtocol.swift.

// MARK: - Hover thumbnail

/// The small live picture shown in a popover while the pointer rests on a running VM's row.
/// The row creates and starts the model when the popover opens and stops it when it closes.
struct VMPreviewThumbnail: View {
    @ObservedObject var preview: VMPreviewModel

    private static let size = CGSize(width: 288, height: 180)

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ZStack {
                Color.black
                if let frame = preview.frame {
                    Image(decorative: frame, scale: 1)
                        .resizable()
                        .interpolation(.medium)
                        .aspectRatio(contentMode: .fit)
                }
                overlay
            }
            .frame(width: Self.size.width, height: Self.size.height)
            .clipShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
            Text(caption)
                .font(.caption2).foregroundStyle(.secondary)
                .lineLimit(2).fixedSize(horizontal: false, vertical: true)
                .frame(width: Self.size.width, alignment: .leading)
        }
        .padding(8)
    }

    @ViewBuilder private var overlay: some View {
        switch preview.status {
        case .unavailable(let reason):
            Text("Preview unavailable: \(reason)")
                .font(.caption).foregroundStyle(.white)
                .multilineTextAlignment(.center)
                .padding(10)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Color.black.opacity(preview.frame == nil ? 0 : 0.6))
        case .connecting where preview.frame == nil, .idle:
            VStack(spacing: 6) {
                ProgressView().controlSize(.small)
                Text("Connecting…").font(.caption).foregroundStyle(.white.opacity(0.8))
            }
        case .live where preview.frame == nil:
            Text("Waiting for the first picture…").font(.caption).foregroundStyle(.white.opacity(0.8))
        default:
            EmptyView()
        }
    }

    private var caption: String {
        switch preview.status {
        case .live: return "Live. Click the row to open a bigger view."
        case .closed: return "Preview paused. Click the row to open a live view."
        case .unavailable: return "Screen Sharing is still available from Connect."
        case .connecting, .idle: return "Looking at the screen…"
        }
    }
}

// MARK: - Live window

/// One window per VM (reopening focuses the existing one). A retained NSWindow, like the other
/// helper windows of this menu-bar app, because the panel it was opened from is a popover
/// that closes as soon as the window takes focus.
@MainActor
final class VMLiveWindowController: NSObject, NSWindowDelegate {
    static let shared = VMLiveWindowController()

    private struct Entry {
        let window: NSWindow
        let model: VMPreviewModel
    }
    private var entries: [String: Entry] = [:]

    func show(_ target: VMPreviewTarget) {
        if let existing = entries[target.id] {
            existing.window.makeKeyAndOrderFront(nil)
            NSApp.activate()
            return
        }
        let model = VMPreviewModel(mode: .window, fetchEndpoint: target.fetchEndpoint)
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 960, height: 640),
                              styleMask: [.titled, .closable, .miniaturizable, .resizable],
                              backing: .buffered, defer: false)
        window.title = "\(target.title) · Live view"
        window.isReleasedWhenClosed = false
        window.minSize = NSSize(width: 480, height: 360)
        window.delegate = self
        let controller = NSHostingController(rootView: VMLiveView(model: model, target: target) { [weak window] in
            window?.performClose(nil)
        })
        controller.sizingOptions = []
        window.contentViewController = controller
        window.setContentSize(NSSize(width: 960, height: 640))
        window.center()
        entries[target.id] = Entry(window: window, model: model)
        window.makeKeyAndOrderFront(nil)
        NSApp.activate()
        model.start()
    }

    func windowWillClose(_ notification: Notification) {
        guard let window = notification.object as? NSWindow,
              let key = entries.first(where: { $0.value.window === window })?.key else { return }
        entries[key]?.model.stop()
        entries[key] = nil
    }
}

struct VMLiveView: View {
    @ObservedObject var model: VMPreviewModel
    let target: VMPreviewTarget
    let close: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            ZStack {
                Color.black
                VMScreenSurface(image: model.frame, inputEnabled: !model.viewOnly,
                                onKey: { sym, down in model.sendKey(keysym: sym, down: down) },
                                onPointer: { buttons, x, y in model.sendPointer(buttons: buttons, x: x, y: y) })
                overlay
            }
            Divider()
            HStack(spacing: 10) {
                Toggle("View only", isOn: $model.viewOnly)
                    .toggleStyle(.switch).controlSize(.small)
                    .help("Turn off to type and click in the virtual machine")
                Text(statusLine)
                    .font(.caption).foregroundStyle(.secondary)
                    .lineLimit(1).truncationMode(.tail)
                Spacer(minLength: 8)
                if canReconnect {
                    Button("Reconnect") { model.restart() }
                }
                Button("Open in Screen Sharing") { target.openScreenSharing() }
                Button("Close") { close() }
            }
            .controlSize(.small)
            .padding(.horizontal, 12).padding(.vertical, 8)
        }
        .frame(minWidth: 480, minHeight: 360)
    }

    @ViewBuilder private var overlay: some View {
        switch model.status {
        case .unavailable(let reason):
            VStack(spacing: 6) {
                Text("Preview unavailable: \(reason)")
                    .font(.callout).foregroundStyle(.white).multilineTextAlignment(.center)
                Text("Screen Sharing is still available from the button below.")
                    .font(.caption).foregroundStyle(.white.opacity(0.7))
            }
            .padding(20)
        case .connecting where model.frame == nil, .idle where model.frame == nil:
            VStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text("Connecting…").font(.callout).foregroundStyle(.white.opacity(0.8))
            }
        case .live where model.frame == nil:
            Text("Waiting for the first picture…").font(.callout).foregroundStyle(.white.opacity(0.8))
        default:
            EmptyView()
        }
    }

    private var canReconnect: Bool {
        switch model.status {
        case .unavailable, .closed: return true
        default: return false
        }
    }

    private var statusLine: String {
        switch model.status {
        case .idle, .connecting:
            return "Connecting…"
        case .live:
            let size = model.framebufferSize
            let dimensions = size.width > 0 ? "\(Int(size.width)) x \(Int(size.height)) · " : ""
            return dimensions + (model.viewOnly ? "view only" : "keyboard and mouse go to the VM")
        case .unavailable(let reason):
            return "Unavailable: \(reason)"
        case .closed:
            return "Disconnected"
        }
    }
}

// MARK: - Screen surface

/// Draws the latest picture scaled to fit, and (only while `inputEnabled`) turns the pointer and
/// keyboard into RFB events. Held keys and buttons are released when input is turned off.
struct VMScreenSurface: NSViewRepresentable {
    let image: CGImage?
    let inputEnabled: Bool
    let onKey: (UInt32, Bool) -> Void
    let onPointer: (UInt8, Int, Int) -> Void

    func makeNSView(context: Context) -> VMScreenNSView {
        let view = VMScreenNSView()
        view.onKey = onKey
        view.onPointer = onPointer
        return view
    }

    func updateNSView(_ view: VMScreenNSView, context: Context) {
        view.onKey = onKey
        view.onPointer = onPointer
        view.image = image
        let turningOn = inputEnabled && !view.inputEnabled
        view.inputEnabled = inputEnabled
        if turningOn {
            DispatchQueue.main.async { view.window?.makeFirstResponder(view) }
        }
    }
}

final class VMScreenNSView: NSView {
    var image: CGImage? {
        didSet { needsDisplay = true }
    }
    var inputEnabled = false {
        didSet { if oldValue && !inputEnabled { releaseAll() } }
    }
    var onKey: ((UInt32, Bool) -> Void)?
    var onPointer: ((UInt8, Int, Int) -> Void)?

    private var pressedKeys: [UInt16: UInt32] = [:]
    private var buttons: UInt8 = 0
    private var lastPoint: (x: Int, y: Int) = (0, 0)
    private var scrollRemainder = CGPoint.zero
    private var trackingArea: NSTrackingArea?

    override var acceptsFirstResponder: Bool { inputEnabled }
    override var isOpaque: Bool { true }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    override func resignFirstResponder() -> Bool {
        releaseAll()
        return super.resignFirstResponder()
    }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let trackingArea { removeTrackingArea(trackingArea) }
        let area = NSTrackingArea(rect: .zero, options: [.mouseMoved, .activeInKeyWindow, .inVisibleRect],
                                  owner: self, userInfo: nil)
        addTrackingArea(area)
        trackingArea = area
    }

    override func draw(_ dirtyRect: NSRect) {
        NSColor.black.setFill()
        NSBezierPath.fill(bounds)
        guard let image, let context = NSGraphicsContext.current?.cgContext else { return }
        let rect = VMScreenGeometry.fitRect(content: CGSize(width: image.width, height: image.height), in: bounds.size)
        guard rect.width > 0, rect.height > 0 else { return }
        context.interpolationQuality = .medium
        context.draw(image, in: rect)
    }

    // MARK: Pointer

    private func framebufferPoint(_ event: NSEvent) -> (x: Int, y: Int)? {
        guard let image else { return nil }
        return VMScreenGeometry.framebufferPoint(
            forViewPoint: convert(event.locationInWindow, from: nil), viewSize: bounds.size,
            framebuffer: CGSize(width: image.width, height: image.height))
    }

    private func sendPointer(_ event: NSEvent) {
        guard inputEnabled, let point = framebufferPoint(event) else { return }
        lastPoint = point
        onPointer?(buttons, point.x, point.y)
    }

    private func press(_ mask: UInt8, down: Bool, _ event: NSEvent) {
        guard inputEnabled else { return }
        if down { buttons |= mask } else { buttons &= ~mask }
        sendPointer(event)
    }

    override func mouseDown(with event: NSEvent) {
        if inputEnabled { window?.makeFirstResponder(self) }
        press(RFBButton.left, down: true, event)
    }
    override func mouseUp(with event: NSEvent) { press(RFBButton.left, down: false, event) }
    override func rightMouseDown(with event: NSEvent) { press(RFBButton.right, down: true, event) }
    override func rightMouseUp(with event: NSEvent) { press(RFBButton.right, down: false, event) }
    override func otherMouseDown(with event: NSEvent) {
        if event.buttonNumber == 2 { press(RFBButton.middle, down: true, event) }
    }
    override func otherMouseUp(with event: NSEvent) {
        if event.buttonNumber == 2 { press(RFBButton.middle, down: false, event) }
    }
    override func mouseMoved(with event: NSEvent) { sendPointer(event) }
    override func mouseDragged(with event: NSEvent) { sendPointer(event) }
    override func rightMouseDragged(with event: NSEvent) { sendPointer(event) }
    override func otherMouseDragged(with event: NSEvent) { sendPointer(event) }

    /// One wheel click per ten points of scrolling (a notched mouse wheel counts ten per notch).
    override func scrollWheel(with event: NSEvent) {
        guard inputEnabled, let point = framebufferPoint(event) else { return }
        let unit: CGFloat = event.hasPreciseScrollingDeltas ? 1 : 10
        scrollRemainder.x += event.scrollingDeltaX * unit
        scrollRemainder.y += event.scrollingDeltaY * unit
        var clicks = 0
        while abs(scrollRemainder.y) >= 10, clicks < 8 {
            let up = scrollRemainder.y > 0
            click(up ? RFBButton.wheelUp : RFBButton.wheelDown, at: point)
            scrollRemainder.y += up ? -10 : 10
            clicks += 1
        }
        while abs(scrollRemainder.x) >= 10, clicks < 8 {
            let left = scrollRemainder.x > 0
            click(left ? RFBButton.wheelLeft : RFBButton.wheelRight, at: point)
            scrollRemainder.x += left ? -10 : 10
            clicks += 1
        }
    }

    private func click(_ wheel: UInt8, at point: (x: Int, y: Int)) {
        onPointer?(buttons | wheel, point.x, point.y)
        onPointer?(buttons, point.x, point.y)
    }

    // MARK: Keyboard

    private func keysym(for event: NSEvent) -> UInt32? {
        if let special = RFBKeyMap.specialKeysym(forKeyCode: event.keyCode) { return special }
        let usesShortcutModifier = !event.modifierFlags.isDisjoint(with: [.control, .command])
        let text = usesShortcutModifier ? event.charactersIgnoringModifiers : event.characters
        guard let scalar = text?.unicodeScalars.first else { return nil }
        return RFBKeyMap.keysym(forScalar: scalar.value)
    }

    /// Sends repeats too: some VNC servers turn the remote key repeat off while a viewer is connected.
    override func keyDown(with event: NSEvent) {
        guard inputEnabled, let sym = keysym(for: event) else { return }
        pressedKeys[event.keyCode] = sym
        onKey?(sym, true)
    }

    override func keyUp(with event: NSEvent) {
        guard let sym = pressedKeys.removeValue(forKey: event.keyCode) else { return }
        onKey?(sym, false)
    }

    private func modifierFlag(forKeyCode code: UInt16) -> NSEvent.ModifierFlags {
        switch code {
        case 0x38, 0x3C: return .shift
        case 0x3B, 0x3E: return .control
        case 0x3A, 0x3D: return .option
        default: return .command
        }
    }

    override func flagsChanged(with event: NSEvent) {
        guard inputEnabled, let sym = RFBKeyMap.modifierKeysym(forKeyCode: event.keyCode) else { return }
        if let held = pressedKeys.removeValue(forKey: event.keyCode) {
            onKey?(held, false)
        } else if event.modifierFlags.contains(modifierFlag(forKeyCode: event.keyCode)) {
            pressedKeys[event.keyCode] = sym
            onKey?(sym, true)
        }
    }

    /// Lets go of every key and button held down, so nothing stays stuck in the VM.
    func releaseAll() {
        for (_, sym) in pressedKeys { onKey?(sym, false) }
        pressedKeys.removeAll()
        if buttons != 0 {
            buttons = 0
            onPointer?(0, lastPoint.x, lastPoint.y)
        }
    }
}
