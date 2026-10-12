import SwiftUI

// What a monitor plugged into the VM would show, for VMs hosted on this Mac: its text console (the virtio
// serial console, hvc0: boot messages, the login prompt, the app installer's progress). Shown on hover when
// there is no graphical screen to show (no desktop, still booting, stopped) or the graphical one is unavailable.

/// A classic 80×24 text screen made from the end of the console log.
enum ConsoleScreen {
    static let columns = 80
    static let rows = 24

    /// The last `rows` rows of `text` as a screen shows them: every line wrapped at `columns`, the oldest scrolled off.
    static func lines(from text: String, columns: Int = ConsoleScreen.columns, rows: Int = ConsoleScreen.rows) -> [String] {
        var out: [String] = []
        // A trailing newline ends the last line; text after it (a login prompt) stays on the last row.
        for raw in text.split(separator: "\n", omittingEmptySubsequences: false) {
            let line = String(raw).replacingOccurrences(of: "\t", with: "    ")
            if line.isEmpty { out.append(""); continue }
            var rest = Substring(line)
            while !rest.isEmpty {
                out.append(String(rest.prefix(columns)))
                rest = rest.dropFirst(columns)
            }
        }
        if out.last == "" { out.removeLast() }
        return Array(out.suffix(rows))
    }
}

/// The inline hover picture of a VM's text console, the same size as the graphical thumbnail.
struct VMConsoleThumbnail: View {
    let url: URL
    let stateText: String?
    let note: String?
    @State private var screen: [String] = []
    @State private var loaded = false

    private static let size = CGSize(width: 288, height: 180)

    init(url: URL, stateText: String?, note: String? = nil) {
        self.url = url
        self.stateText = stateText
        self.note = note
    }

    private func refresh() async {
        let url = self.url
        let text = await Task.detached { SandboxLogReader.tail(of: url, maxBytes: 8 * 1024) }.value
        let lines = text.map { ConsoleScreen.lines(from: $0) } ?? []
        if lines != screen { screen = lines }
        loaded = true
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ZStack(alignment: .topLeading) {
                Color.black
                if screen.isEmpty {
                    Text(loaded ? "No console output yet." : "Reading the console…")
                        .font(.caption).foregroundStyle(.white.opacity(0.7)).padding(10)
                } else {
                    // 80 columns of 6 pt monospace fill the 288 pt width, like a real 80×24 screen.
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(Array(screen.enumerated()), id: \.offset) { _, line in
                            Text(line.isEmpty ? " " : line)
                                .font(.system(size: 6, design: .monospaced))
                                .foregroundStyle(Color(red: 0.85, green: 0.9, blue: 0.85))
                                .lineLimit(1).fixedSize()
                        }
                    }
                    .padding(.leading, 2)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottomLeading)
                }
            }
            .frame(width: Self.size.width, height: Self.size.height)
            .clipShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
            Text(caption)
                .font(.caption2).foregroundStyle(.secondary)
                .lineLimit(2).fixedSize(horizontal: false, vertical: true)
                .frame(width: Self.size.width, alignment: .leading)
        }
        .padding(8)
        .task {
            while !Task.isCancelled {
                await refresh()
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    private var caption: String {
        if let note { return note }
        if stateText == "running" || stateText == "paused" || stateText == "provisioning" {
            return "Text console, as a monitor would show it. Updates live."
        }
        return "Text console, last output before it stopped."
    }
}

/// The graphical hover picture, falling back to the text console when the screen cannot be shown.
struct VMHoverScreen: View {
    @ObservedObject var preview: VMPreviewModel
    let console: URL?
    let stateText: String?
    let openLive: () -> Void

    var body: some View {
        if case .unavailable = preview.status, let console {
            VMConsoleThumbnail(url: console, stateText: stateText,
                               note: "No graphical screen yet. Showing the text console, as a monitor would.")
        } else {
            VMPreviewThumbnail(preview: preview)
                .contentShape(Rectangle())
                .onTapGesture(perform: openLive)
        }
    }
}
