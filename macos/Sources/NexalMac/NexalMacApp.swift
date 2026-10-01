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
        Settings {
            ConnectorSettingsView(loginItem: appDelegate.loginItem)
                .environmentObject(appDelegate.preferences)
                .environmentObject(appDelegate.model)
        }
            .commands {
                CommandGroup(replacing: .appSettings) {
                    Button("Settings…") { appDelegate.showSettings() }.keyboardShortcut(",")
                }
                CommandGroup(replacing: .appInfo) {
                    Button("About neXal@home") { appDelegate.showAbout() }
                }
            }
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

/// Launch shows the splash, then stays in the menu bar. Windows open only in
/// response to an explicit user action, including reopening from Finder.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate {
    let model = AppModel()
    let loginItem = LoginItemSettings()
    let preferences = ConnectorPreferences()
    private var settingsWindow: NSWindow?
    private var window: NSWindow?
    private var splashWindow: NSWindow?
    private var statusItem: NSStatusItem?
    private let popover = NSPopover()
    private let hintPopover = NSPopover()
    private var hintWindow: NSWindow?
    private var modelChanges: AnyCancellable?
    private var blinkTimer: Timer?
    private var blinkOn = true
    private var shownBadge = ""
    private var launchFinished = false
    /// Whether this Mac was paired at the last model change; nil until the first
    /// reading, so an already-paired Mac does not chime at launch.
    private var wasLinked: Bool?

    static let splashSeconds: TimeInterval = 2
    private static let hintShownKey = "menuBarHintShown"
    static let panelSize = NSSize(width: 460, height: 700)

    // MARK: Lifecycle

    func applicationWillFinishLaunching(_ notification: Notification) {
        // Start as a menu-bar app; the main window promotes it to a regular app
        // for as long as the window is open.
        NSApp.setActivationPolicy(.accessory)
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        installStatusItem()
        loginItem.configureFirstLaunch()
        if preferences.showSplash {
            showSplash { [weak self] in
                self?.launchFinished = true
            }
        } else {
            // Keep launch-time reopen events from opening a main window.
            DispatchQueue.main.async { [weak self] in self?.launchFinished = true }
        }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        // Ignore launch-time reopen events while the splash is visible.
        if launchFinished { showWindow() }
        return true
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    // MARK: Main window

    func showWindow() {
        popover.performClose(nil)
        dismissHint()
        let window = self.window ?? makeWindow()
        self.window = window
        NSApp.setActivationPolicy(.regular)
        if !window.isVisible { window.center() }
        window.makeKeyAndOrderFront(nil)
        // orderFrontRegardless covers the case where activation is refused
        // (another app is mid-interaction); the window is still in front.
        window.orderFrontRegardless()
        NSApp.activate()
    }

    private func makeWindow() -> NSWindow {
        let hosting = NSHostingController(rootView: NetworkPanel(showSettings: { [weak self] in self?.showSettings() },
                                                               showAbout: { [weak self] in self?.showAbout() }).environmentObject(model).environmentObject(preferences))
        // Size from the content up front, so the window never appears at a
        // default size and then jumps to the panel's size.
        hosting.sizingOptions = [.preferredContentSize]
        let window = NSWindow(contentRect: NSRect(origin: .zero, size: Self.panelSize),
                              styleMask: [.titled, .closable, .miniaturizable],
                              backing: .buffered, defer: false)
        window.contentViewController = hosting
        window.setContentSize(Self.panelSize)
        window.title = "neXal@home"
        window.isReleasedWhenClosed = false
        window.tabbingMode = .disallowed
        window.collectionBehavior.insert(.fullScreenNone)
        window.setFrameAutosaveName("neXalConnectorMain")
        window.delegate = self
        return window
    }

    func windowWillClose(_ notification: Notification) {
        guard (notification.object as? NSWindow) === window else { return }
        // Back to menu bar only: no Dock icon, no app menu. Deferred one turn so
        // the close animation finishes before the policy changes.
        DispatchQueue.main.async { [weak self] in
            NSApp.setActivationPolicy(.accessory)
            self?.showMenuBarHintIfNeeded()
        }
    }

    // MARK: Splash

    private func showSplash(then next: @escaping () -> Void) {
        let size = NSSize(width: 360, height: 230)
        let splash = NSWindow(contentRect: NSRect(origin: .zero, size: size),
                              styleMask: [.borderless], backing: .buffered, defer: false)
        let hosting = NSHostingController(rootView: SplashView())
        hosting.sizingOptions = []
        splash.contentViewController = hosting
        splash.setContentSize(size)
        splash.isOpaque = false
        splash.backgroundColor = .clear
        splash.hasShadow = true
        splash.level = .floating
        splash.isReleasedWhenClosed = false
        splash.ignoresMouseEvents = true
        splash.alphaValue = 0
        splash.center()
        splash.orderFrontRegardless()
        splashWindow = splash
        NSAnimationContext.runAnimationGroup { $0.duration = 0.2; splash.animator().alphaValue = 1 }
        DispatchQueue.main.asyncAfter(deadline: .now() + Self.splashSeconds) { [weak self] in
            NSAnimationContext.runAnimationGroup({ $0.duration = 0.25; splash.animator().alphaValue = 0 },
                                                 completionHandler: {
                MainActor.assumeIsolated {
                    splash.orderOut(nil)
                    self?.splashWindow = nil
                    next()
                }
            })
        }
    }

    // MARK: Menu bar

    private func installStatusItem() {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        item.autosaveName = "neXalConnectorStatusItem"
        item.button?.target = self
        item.button?.action = #selector(togglePopover(_:))
        item.button?.sendAction(on: [.leftMouseUp, .rightMouseUp])
        item.button?.setAccessibilityLabel("neXal network")
        statusItem = item
        popover.behavior = .transient
        popover.contentViewController = NSHostingController(rootView: NetworkPanel(showSettings: { [weak self] in self?.showSettings() },
                                                               showAbout: { [weak self] in self?.showAbout() }).environmentObject(model).environmentObject(preferences))
        refreshBadge()
        // objectWillChange fires before the change lands; read the new value on
        // the next turn of the run loop. refreshBadge ignores unchanged states.
        modelChanges = model.objectWillChange.merge(with: preferences.objectWillChange).sink { [weak self] _ in
            DispatchQueue.main.async { self?.refreshBadge() }
        }
        blinkTimer = Timer.scheduledTimer(withTimeInterval: 0.65, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                if self.model.menuBarSeverity == .bad && self.preferences.flashAlerts { self.blinkOn.toggle() } else { self.blinkOn = true }
                self.refreshBadge()
            }
        }
    }

    private func refreshBadge() {
        let linked = model.isLinked
        if wasLinked == false, linked, preferences.playPairingSound { PairingChime.play() }
        wasLinked = linked
        let severity = model.menuBarSeverity
        let visible = severity != .bad || !preferences.flashAlerts || blinkOn
        let key = "\(severity)-\(visible)"
        guard key != shownBadge, let button = statusItem?.button else { return }
        shownBadge = key
        button.image = MenuBarBadge.image(severity, visible: visible)
        button.setAccessibilityLabel(severity == .bad ? "neXal needs attention" : "neXal network")
    }

    @objc private func togglePopover(_ sender: Any?) {
        guard let button = statusItem?.button else { return }
        if NSApp.currentEvent?.type == .rightMouseUp {
            popover.performClose(nil)
            let menu = NSMenu()
            for (title, action) in [("Open Connector", #selector(openConnector)),
                                    ("Settings…", #selector(showSettings)),
                                    ("About neXal@home", #selector(showAbout))] {
                let entry = menu.addItem(withTitle: title, action: action, keyEquivalent: "")
                entry.target = self
            }
            let hosting = ThrowawayHosting.shared
            hosting.reloadLocal()
            if !hosting.runningHere.isEmpty {
                menu.addItem(.separator())
                let info = menu.addItem(withTitle: hosting.runningLine, action: nil, keyEquivalent: "")
                info.isEnabled = false
                menu.addItem(withTitle: "Stop throwaway hosts", action: #selector(stopThrowawayHosts), keyEquivalent: "").target = self
            }
            menu.addItem(.separator())
            menu.addItem(withTitle: "Quit", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q").target = NSApp
            itemMenu(menu, button: button)
            return
        }
        dismissHint()
        if popover.isShown {
            popover.performClose(sender)
        } else {
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            popover.contentViewController?.view.window?.makeKey()
            NSApp.activate()
        }
    }

    private func itemMenu(_ menu: NSMenu, button: NSStatusBarButton) {
        statusItem?.menu = menu
        button.performClick(nil)
        statusItem?.menu = nil
    }

    @objc private func openConnector() { showWindow() }

    @objc private func stopThrowawayHosts() { ThrowawayHosting.shared.stopAll() }

    @objc func showSettings() {
        popover.performClose(nil)
        dismissHint()
        loginItem.refresh()
        if settingsWindow == nil {
            let controller = NSHostingController(rootView: ConnectorSettingsView(loginItem: loginItem)
                .environmentObject(preferences).environmentObject(model))
            let window = NSWindow(contentViewController: controller)
            window.styleMask = [.titled, .closable]
            window.title = "neXal@home Settings"
            window.isReleasedWhenClosed = false
            window.center()
            settingsWindow = window
        }
        settingsWindow?.makeKeyAndOrderFront(nil)
        NSApp.activate()
    }

    @objc func showAbout() {
        popover.performClose(nil)
        dismissHint()
        NSApp.orderFrontStandardAboutPanel(options: [
            .applicationName: "neXal@home",
            .credits: NSAttributedString(string: "Connect this Mac to your neXal network.")
        ])
        NSApp.activate()
    }

    // MARK: "Find me in the menu bar" callout

    /// Whether the menu-bar icon is actually on screen. It is not when macOS has
    /// hidden it (Menu Bar settings, a notch, or a crowded bar).
    private var statusItemIsOnScreen: Bool {
        guard let item = statusItem, item.isVisible, let buttonWindow = item.button?.window else { return false }
        return buttonWindow.occlusionState.contains(.visible) && buttonWindow.screen != nil
    }

    private func showMenuBarHintIfNeeded() {
        let onScreen = statusItemIsOnScreen
        // Shown once; always shown when the icon cannot be seen, because then the
        // owner has no other way back except reopening the app.
        guard !UserDefaults.standard.bool(forKey: Self.hintShownKey) || !onScreen else { return }
        UserDefaults.standard.set(true, forKey: Self.hintShownKey)
        let hint = MenuBarHintView(badge: MenuBarBadge.image(model.menuBarSeverity, visible: true),
                                   iconHidden: !onScreen) { [weak self] in self?.dismissHint() }
        if onScreen, let button = statusItem?.button {
            hintPopover.behavior = .transient
            hintPopover.animates = true
            hintPopover.contentViewController = NSHostingController(rootView: hint)
            hintPopover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
        } else {
            let hosting = NSHostingController(rootView: hint)
            let panel = NSPanel(contentViewController: hosting)
            panel.styleMask = [.titled, .closable, .fullSizeContentView]
            panel.titlebarAppearsTransparent = true
            panel.titleVisibility = .hidden
            panel.isReleasedWhenClosed = false
            panel.level = .floating
            panel.center()
            panel.orderFrontRegardless()
            hintWindow = panel
        }
    }

    private func dismissHint() {
        hintPopover.performClose(nil)
        hintWindow?.orderOut(nil)
        hintWindow = nil
    }
}

/// The launch splash: the neXal wordmark on the brand gradient.
private struct SplashView: View {
    var body: some View {
        ZStack {
            LinearGradient(colors: [.black, Color(red: 0.04, green: 0.12, blue: 0.20)],
                           startPoint: .topLeading, endPoint: .bottomTrailing)
            VStack(spacing: -2) {
                Text("neXal").font(.system(size: 40, weight: .semibold, design: .rounded))
                Text("systems").font(.system(size: 17, weight: .medium, design: .rounded)).tracking(5)
            }
            .foregroundStyle(.white)
        }
        .frame(width: 360, height: 230)
        .clipShape(RoundedRectangle(cornerRadius: 18, style: .continuous))
        .accessibilityElement(children: .combine)
        .accessibilityLabel("neXal systems")
    }
}

/// Shows where the app lives after its window closes: a small picture of the
/// menu bar with the neXal icon highlighted, and one line of explanation.
private struct MenuBarHintView: View {
    let badge: NSImage
    let iconHidden: Bool
    let dismiss: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            menuBarPicture
            Text("neXal@home is still running")
                .font(.headline)
            Text(iconHidden
                 ? "Its icon is hidden right now. Turn on neXal@home in System Settings › Menu Bar, or open the app again from Applications."
                 : "Click this icon in the menu bar at any time to see your network and connections.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                if iconHidden {
                    Button("Open Menu Bar Settings") {
                        if let url = URL(string: "x-apple.systempreferences:com.apple.ControlCenter-Settings.extension") {
                            NSWorkspace.shared.open(url)
                        }
                        dismiss()
                    }
                }
                Spacer()
                Button("Got it", action: dismiss)
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(18)
        .frame(width: 320)
    }

    /// A stylised slice of the menu bar: a few system icons and the clock, with
    /// the neXal icon ringed so the eye goes straight to it.
    private var menuBarPicture: some View {
        HStack(spacing: 12) {
            Spacer(minLength: 0)
            Image(nsImage: badge)
                .padding(5)
                .background(Circle().fill(Color.accentColor.opacity(0.18)))
                .overlay(Circle().strokeBorder(Color.accentColor, lineWidth: 1.5))
                .accessibilityLabel("neXal icon")
            Image(systemName: "wifi")
            Image(systemName: "battery.75percent")
            Image(systemName: "switch.2")
            Text("Fri 9:41").monospacedDigit()
        }
        .font(.system(size: 13))
        .foregroundStyle(.primary.opacity(0.75))
        .padding(.horizontal, 12)
        .frame(height: 34)
        .background(RoundedRectangle(cornerRadius: 8, style: .continuous).fill(.quaternary))
        .accessibilityElement(children: .contain)
    }
}
