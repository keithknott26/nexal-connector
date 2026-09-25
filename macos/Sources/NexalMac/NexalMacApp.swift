import AppKit
import SwiftUI

@main
struct NexalMacApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        MenuBarExtra {
            NetworkPanel().environmentObject(appDelegate.model)
        } label: {
            MenuBarLabel(model: appDelegate.model)
        }
        .menuBarExtraStyle(.window)
    }
}

/// Observes the model so the menu-bar symbol follows connection state.
private struct MenuBarLabel: View {
    @ObservedObject var model: AppModel
    var body: some View {
        TimelineView(.periodic(from: .now, by: 0.65)) { context in
            let visible = model.menuBarSeverity != .bad || Int(context.date.timeIntervalSince1970 * 2) % 2 == 0
            if let image = Self.badge(model.menuBarSeverity, visible: visible) {
                Image(nsImage: image)
                    .accessibilityLabel(model.menuBarSeverity == .bad ? "neXal needs attention" : "neXal network")
            }
        }
    }

    /// A brand-specific circled @ drawn locally, without an asset or generated
    /// bitmap. Red attention state alternates with an exclamation mark.
    static func badge(_ severity: IndicatorSeverity, visible: Bool) -> NSImage? {
        let color: NSColor
        switch severity {
        case .good: color = .systemGreen
        case .pending: color = .systemYellow
        case .warning: color = .systemOrange
        case .bad: color = .systemRed
        case .inactive: color = .secondaryLabelColor
        }
        let image = NSImage(size: NSSize(width: 18, height: 18), flipped: false) { rect in
            color.withAlphaComponent(visible ? 1 : 0.28).setStroke()
            let ring = NSBezierPath(ovalIn: rect.insetBy(dx: 1.5, dy: 1.5))
            ring.lineWidth = 1.6
            ring.stroke()
            let text = severity == .bad && visible ? "!" : "@"
            let style = NSMutableParagraphStyle(); style.alignment = .center
            let attrs: [NSAttributedString.Key: Any] = [
                .foregroundColor: color.withAlphaComponent(visible ? 1 : 0.28),
                .font: NSFont.systemFont(ofSize: 11, weight: .bold), .paragraphStyle: style]
            text.draw(in: NSRect(x: 0, y: 2.2, width: 18, height: 13), withAttributes: attrs)
            return true
        }
        image.isTemplate = false
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

    func applicationDidFinishLaunching(_ notification: Notification) {
        showSplash()
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        showWindow()
        return true
    }

    func showWindow() {
        if window == nil {
            let hosting = NSHostingController(rootView: NetworkPanel().environmentObject(model))
            let window = NSWindow(contentViewController: hosting)
            window.title = "neXal Connector"
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
