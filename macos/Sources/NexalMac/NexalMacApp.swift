import AppKit
import Combine
import SwiftUI

@main
struct NexalMacApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    /// The menu-bar item is an AppKit NSStatusItem owned by AppDelegate, not a
    /// SwiftUI MenuBarExtra. With a MenuBarExtra label that draws a coloured
    /// image, SwiftUI set the status-item image, which invalidated the label,
    /// which set the image again: an update loop on the main thread from launch
    /// (100% CPU, no icon, no window). The status item is updated only when the
    /// indicator actually changes. This scene exists only because an App needs one.
    var body: some Scene {
        Settings { EmptyView() }
    }
}

/// The menu-bar indicator: a circled @ tinted by network health, drawn once per
/// state into a bitmap and reused.
enum MenuBarBadge {
    @MainActor private static var cache: [String: NSImage] = [:]

    @MainActor static func image(_ severity: IndicatorSeverity, visible: Bool) -> NSImage {
        let key = "\(severity)-\(visible)"
        if let cached = cache[key] { return cached }
        let color: NSColor
        switch severity {
        case .good: color = .systemGreen
        case .pending: color = .systemYellow
        case .warning: color = .systemOrange
        case .bad: color = .systemRed
        case .inactive: color = .secondaryLabelColor
        }
        let size = NSSize(width: 18, height: 18)
        // A 2x bitmap drawn once: a lazily drawn (drawingHandler) image is
        // re-resolved by the status bar on every layout pass.
        guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 36, pixelsHigh: 36,
                                         bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                         colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)
        else { return NSImage(size: size) }
        rep.size = size // before the context: this is what makes drawing 2x
        guard let context = NSGraphicsContext(bitmapImageRep: rep) else { return NSImage(size: size) }
        NSGraphicsContext.saveGraphicsState()
        NSGraphicsContext.current = context
        color.withAlphaComponent(visible ? 1 : 0.28).setStroke()
        let ring = NSBezierPath(ovalIn: NSRect(origin: .zero, size: size).insetBy(dx: 1.5, dy: 1.5))
        ring.lineWidth = 1.6
        ring.stroke()
        let text = severity == .bad && visible ? "!" : "@"
        let style = NSMutableParagraphStyle(); style.alignment = .center
        let attrs: [NSAttributedString.Key: Any] = [
            .foregroundColor: color.withAlphaComponent(visible ? 1 : 0.28),
            .font: NSFont.systemFont(ofSize: 11, weight: .bold), .paragraphStyle: style]
        text.draw(in: NSRect(x: 0, y: 2.2, width: 18, height: 13), withAttributes: attrs)
        NSGraphicsContext.restoreGraphicsState()
        let image = NSImage(size: size)
        image.addRepresentation(rep)
        image.isTemplate = false
        cache[key] = image
        return image
    }
}

/// A menu-bar-only app is easy to lose: on a MacBook with a notch, or a crowded
/// menu bar, its icon can be hidden, and launching the app then appears to do
/// nothing. So the same panel is also shown in an ordinary window when this Mac
/// is not paired yet (the owner needs the pairing code) and whenever the app is
/// opened again from Finder, Launchpad or Spotlight while it is running.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let model = AppModel()
    private var window: NSWindow?
    private var splashWindow: NSWindow?
    private var statusItem: NSStatusItem?
    private let popover = NSPopover()
    private var modelChanges: AnyCancellable?
    private var blinkTimer: Timer?
    private var blinkOn = true
    private var shownBadge = ""

    func applicationDidFinishLaunching(_ notification: Notification) {
        installStatusItem()
        showSplash()
        // Unpaired, the owner needs the pairing code, and the menu-bar icon may be
        // hidden (notch, crowded bar, or not yet allowed in System Settings).
        if !model.hasPersistedHostIdentity {
            DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in self?.showWindow() }
        }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        showWindow()
        return true
    }

    private func installStatusItem() {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        item.button?.target = self
        item.button?.action = #selector(togglePopover(_:))
        item.button?.setAccessibilityLabel("neXal network")
        statusItem = item
        popover.behavior = .transient
        popover.contentViewController = NSHostingController(rootView: NetworkPanel().environmentObject(model))
        refreshBadge()
        // objectWillChange fires before the change lands; read the new value on
        // the next turn of the run loop. refreshBadge ignores unchanged states.
        modelChanges = model.objectWillChange.sink { [weak self] _ in
            DispatchQueue.main.async { self?.refreshBadge() }
        }
        blinkTimer = Timer.scheduledTimer(withTimeInterval: 0.65, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                if self.model.menuBarSeverity == .bad { self.blinkOn.toggle() } else { self.blinkOn = true }
                self.refreshBadge()
            }
        }
    }

    private func refreshBadge() {
        let severity = model.menuBarSeverity
        let visible = severity != .bad || blinkOn
        let key = "\(severity)-\(visible)"
        guard key != shownBadge, let button = statusItem?.button else { return }
        shownBadge = key
        button.image = MenuBarBadge.image(severity, visible: visible)
        button.setAccessibilityLabel(severity == .bad ? "neXal needs attention" : "neXal network")
    }

    @objc private func togglePopover(_ sender: Any?) {
        guard let button = statusItem?.button else { return }
        if popover.isShown {
            popover.performClose(sender)
        } else {
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            popover.contentViewController?.view.window?.makeKey()
            NSApp.activate(ignoringOtherApps: true)
        }
    }

    func showWindow() {
        if window == nil {
            let hosting = NSHostingController(rootView: NetworkPanel().environmentObject(model))
            let window = NSWindow(contentViewController: hosting)
            window.title = "neXal-Connector"
            window.styleMask = [.titled, .closable, .miniaturizable]
            window.isReleasedWhenClosed = false
            window.center()
            self.window = window
        }
        NSApp.activate(ignoringOtherApps: true)
        window?.makeKeyAndOrderFront(nil)
    }

    private func showSplash() {
        let root = ZStack {
            LinearGradient(colors: [.black, Color(red: 0.04, green: 0.12, blue: 0.20)],
                           startPoint: .topLeading, endPoint: .bottomTrailing)
            VStack(spacing: -2) {
                Text("neXal").font(.system(size: 40, weight: .semibold, design: .rounded))
                Text("systems").font(.system(size: 17, weight: .medium, design: .rounded)).tracking(5)
            }.foregroundStyle(.white)
        }.frame(width: 360, height: 230)
        let splash = NSWindow(contentViewController: NSHostingController(rootView: root))
        splash.styleMask = [.borderless]
        splash.isOpaque = false
        splash.backgroundColor = .clear
        splash.level = .floating
        splash.center()
        splash.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        splashWindow = splash
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self, weak splash] in
            splash?.orderOut(nil)
            self?.splashWindow = nil
        }
    }
}
