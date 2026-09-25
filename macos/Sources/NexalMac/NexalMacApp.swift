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
        // A plain SF Symbol label is drawn as a template (monochrome) in the menu
        // bar, so colour must come from a non-template image.
        if let image = Self.tinted(model.menuBarSymbol, model.menuBarSeverity) {
            Image(nsImage: image).accessibilityLabel("neXal")
        } else {
            Label("neXal", systemImage: model.menuBarSymbol)
        }
    }

    static func tinted(_ symbol: String, _ severity: IndicatorSeverity) -> NSImage? {
        let color: NSColor
        switch severity {
        case .good: color = .systemGreen
        case .pending: color = .systemYellow
        case .warning: color = .systemOrange
        case .bad: color = .systemRed
        case .inactive: return nil // grey = the normal template look
        }
        let config = NSImage.SymbolConfiguration(pointSize: 14, weight: .regular)
            .applying(NSImage.SymbolConfiguration(paletteColors: [color]))
        guard let image = NSImage(systemSymbolName: symbol, accessibilityDescription: "neXal")?
            .withSymbolConfiguration(config) else { return nil }
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

    func applicationDidFinishLaunching(_ notification: Notification) {
        if !model.hasPersistedHostIdentity { showWindow() }
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
}
