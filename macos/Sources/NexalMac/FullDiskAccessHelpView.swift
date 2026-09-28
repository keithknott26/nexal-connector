import AppKit
import SwiftUI

/// The user grants Full Disk Access in System Settings; never modify TCC.
struct FullDiskAccessHelpView: View {
    var onClose: () -> Void
    private let appURL = Bundle.main.bundleURL

    var body: some View {
        HStack(spacing: 18) {
            VStack(spacing: 4) {
                Image(systemName: "arrow.up").foregroundStyle(.secondary)
                Image(nsImage: NSWorkspace.shared.icon(forFile: appURL.path))
                    .resizable().frame(width: 64, height: 64)
                    .onDrag { NSItemProvider(object: appURL as NSURL) }
                    .accessibilityLabel("Drag neXal-Connector into Full Disk Access")
                Text("Drag me").font(.caption.bold())
            }
            VStack(alignment: .leading, spacing: 8) {
                Text("Allow Full Disk Access").font(.headline)
                Text("If neXal is missing, drag its icon into the list and enable it. Already enabled? Quit and reopen neXal, then retry.")
                    .fixedSize(horizontal: false, vertical: true)
                Text("Needed to set up Time Machine. Reopen neXal if macOS asks.")
                    .font(.caption).foregroundStyle(.secondary)
                HStack {
                    Button("Show in Finder") { NSWorkspace.shared.activateFileViewerSelecting([appURL]) }
                    Spacer()
                    Button("Done", action: onClose).keyboardShortcut(.cancelAction)
                }.controlSize(.small)
            }
        }
        .padding(18)
        .frame(width: 440)
        .accessibilityIdentifier("full-disk-access-help")
    }
}

/// A popover closes when Settings activates. Keep the draggable application visible
/// in a separately retained panel, without claiming or modifying permission state.
@MainActor
final class PermissionHelpWindow: NSObject, NSWindowDelegate {
    static let shared = PermissionHelpWindow()
    private var panel: NSPanel?
    private var placementTimer: Timer?

    func showFullDiskAccess() {
        if let panel {
            panel.makeKeyAndOrderFront(nil)
            openSettings()
            return
        }
        let panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 476, height: 165),
            styleMask: [.titled, .closable, .utilityWindow],
            backing: .buffered, defer: false
        )
        panel.title = "neXal · Full Disk Access"
        panel.delegate = self
        panel.isReleasedWhenClosed = false
        panel.hidesOnDeactivate = false
        panel.level = .floating
        panel.collectionBehavior = [.moveToActiveSpace, .fullScreenAuxiliary]
        panel.contentView = NSHostingView(rootView: FullDiskAccessHelpView { [weak panel] in
            panel?.close()
        })
        panel.center()
        self.panel = panel
        panel.makeKeyAndOrderFront(nil)
        openSettings()
    }

    private func openSettings() {
        if let panel, let bounds = NSScreen.main?.visibleFrame {
            panel.setFrameOrigin(NSPoint(x: bounds.midX - panel.frame.width / 2, y: bounds.minY + 12))
        }
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles") {
            NSWorkspace.shared.open(url)
        }
        placementTimer?.invalidate()
        placementTimer = Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.placeBelowSettings() }
        }
    }

    func windowWillClose(_ notification: Notification) {
        placementTimer?.invalidate()
        placementTimer = nil
    }

    // Read window bounds only, without Accessibility or screen capture access.
    // Do not move or operate the user's System Settings window.
    private func placeBelowSettings() {
        guard let panel, panel.isVisible,
              let settings = NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.systempreferences").first,
              let windows = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]],
              let info = windows.first(where: {
                  ($0[kCGWindowOwnerPID as String] as? Int32) == settings.processIdentifier &&
                  ($0[kCGWindowLayer as String] as? Int) == 0
              }),
              let dictionary = info[kCGWindowBounds as String] as? NSDictionary,
              let quartz = CGRect(dictionaryRepresentation: dictionary),
              let primary = NSScreen.screens.first else { return }
        let frame = NSRect(x: quartz.minX, y: primary.frame.maxY - quartz.maxY,
                           width: quartz.width, height: quartz.height)
        guard let screen = NSScreen.screens.first(where: { $0.frame.intersects(frame) }) else { return }
        let bounds = screen.visibleFrame
        let x = min(max(frame.midX - panel.frame.width / 2, bounds.minX), bounds.maxX - panel.frame.width)
        let y = max(bounds.minY, frame.minY - panel.frame.height - 8)
        let origin = NSPoint(x: x, y: y)
        if panel.frame.origin != origin { panel.setFrameOrigin(origin) }
    }
}
