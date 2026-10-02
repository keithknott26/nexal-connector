import AppKit
import Combine
import SwiftUI
import UniformTypeIdentifiers

/// Settings › General › Diagnostics: diagnostic mode, the logs, and a redacted
/// bundle the owner can read or send. Everything goes through `nexal
/// diagnostics`, the same bounded CLI seam as every other command.
struct DiagnosticsSettingsView: View {
    @EnvironmentObject private var model: AppModel
    @State private var enabled = AppDiagnostics.isEnabled
    @State private var busy = false
    @State private var error: String?
    @State private var note: String?
    @State private var showsLog = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Toggle("Diagnostic mode", isOn: Binding(
                get: { enabled },
                set: { on in Task { await perform(on ? .on : .off) } }
            ))
            .disabled(busy)
            Text("Records detailed connection, post-quantum, wake, coordinator and service events in diagnostics.log so you can see what went wrong. The connector switches within a few seconds, with no restart. Passwords, keys and tokens are never logged.")
                .font(.caption).foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("Open diagnostic log") { openInConsole() }
                Button("View log…") {
                    AppDiagnostics.ui("log viewer opened")
                    showsLog = true
                }
                Button("Export diagnostics…") { Task { await export() } }
                    .disabled(busy)
            }
            if busy { ProgressView().controlSize(.small) }
            if let note { Text(note).font(.caption).foregroundStyle(.secondary).textSelection(.enabled) }
            if let error { Text(error).font(.caption).foregroundStyle(.orange).textSelection(.enabled) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .sheet(isPresented: $showsLog) { DiagnosticsLogView() }
        .task { await perform(.status) }
    }

    private func perform(_ action: DiagnosticsAction) async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        do {
            let reply = try await model.diagnostics(action)
            enabled = reply.enabled
            error = nil
            if action == .on || action == .off {
                AppDiagnostics.ui("diagnostic mode toggled", ["enabled": String(reply.enabled)])
                note = reply.enabled ? "Diagnostic mode is on. The connector will log in detail within a few seconds." : nil
            }
        } catch {
            enabled = AppDiagnostics.isEnabled
            self.error = error.localizedDescription
            AppDiagnostics.error("diagnostics command failed", ["error": error.localizedDescription])
        }
    }

    /// Console.app shows the log live; fall back to the default viewer.
    private func openInConsole() {
        let fm = FileManager.default
        let url = fm.fileExists(atPath: AppDiagnostics.diagnosticsLogURL.path) ? AppDiagnostics.diagnosticsLogURL : AppDiagnostics.agentLogURL
        guard fm.fileExists(atPath: url.path) else {
            error = "No log yet. Turn on diagnostic mode, wait a few seconds, then try again."
            return
        }
        AppDiagnostics.ui("diagnostic log opened", ["file": url.lastPathComponent])
        if let console = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "com.apple.Console") {
            NSWorkspace.shared.open([url], withApplicationAt: console, configuration: NSWorkspace.OpenConfiguration())
        } else {
            NSWorkspace.shared.open(url)
        }
    }

    private func export() async {
        let panel = NSSavePanel()
        let stamp = ISO8601DateFormatter.string(from: Date(), timeZone: .current, formatOptions: [.withFullDate])
        panel.nameFieldStringValue = "nexal-diagnostics-\(stamp).txt"
        panel.allowedContentTypes = [.plainText]
        panel.canCreateDirectories = true
        panel.message = "The bundle is redacted: no passwords, keys, tokens or MAC addresses. Review it before you share it."
        guard panel.runModal() == .OK, let url = panel.url else { return }
        guard !busy else { return }
        busy = true
        defer { busy = false }
        do {
            let reply = try await model.diagnostics(.bundle(path: url.path))
            error = nil
            note = "Saved \(url.lastPathComponent)."
            AppDiagnostics.ui("diagnostics exported", ["file": url.lastPathComponent])
            if reply.bundle != nil { NSWorkspace.shared.activateFileViewerSelecting([url]) }
        } catch {
            self.error = error.localizedDescription
            AppDiagnostics.error("diagnostics export failed", ["error": error.localizedDescription])
        }
    }
}

/// One line of a log file, for the in-app viewer.
struct DiagnosticsLogLine: Identifiable {
    let id: Int
    let source: String
    let category: String
    let level: String
    let date: Date?
    let text: String
}

/// Reads and parses the three logs. Nonisolated: runs off the main actor.
enum DiagnosticsLogReader {
    static let tailBytes = 512 << 10
    static let maxLines = 3000

    static func tail(_ url: URL) -> String {
        guard let handle = try? FileHandle(forReadingFrom: url) else { return "" }
        defer { try? handle.close() }
        let size = (try? handle.seekToEnd()) ?? 0
        let start = size > UInt64(tailBytes) ? size - UInt64(tailBytes) : 0
        try? handle.seek(toOffset: start)
        var text = String(decoding: (try? handle.readToEnd()) ?? Data(), as: UTF8.self)
        if start > 0, let newline = text.firstIndex(of: "\n") { text = String(text[text.index(after: newline)...]) }
        return text
    }

    static func parseDate(_ s: String) -> Date? {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = f.date(from: s) { return d }
        f.formatOptions = [.withInternetDateTime]
        return f.date(from: s)
    }

    /// Lines from the chosen sources, oldest first (newest last).
    static func load(_ sources: [(name: String, url: URL)]) -> [DiagnosticsLogLine] {
        var all: [DiagnosticsLogLine] = []
        var id = 0
        for source in sources {
            var lastDate: Date?
            for raw in tail(source.url).split(separator: "\n", omittingEmptySubsequences: true) {
                let line = String(raw)
                var category = source.name
                var level = ""
                var date: Date?
                if line.hasPrefix("{"), let data = line.data(using: .utf8),
                   let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
                    if let c = object["component"] as? String { category = c }
                    else if let c = object["category"] as? String { category = c }
                    else if object["app"] != nil { category = "app" }
                    level = (object["level"] as? String) ?? ""
                    if let t = object["time"] as? String { date = parseDate(t) }
                }
                if date == nil { date = lastDate }
                lastDate = date
                all.append(DiagnosticsLogLine(id: id, source: source.name, category: category, level: level, date: date, text: line))
                id += 1
            }
        }
        if sources.count > 1 {
            all.sort { ($0.date ?? Date.distantPast, $0.id) < ($1.date ?? Date.distantPast, $1.id) }
        }
        return Array(all.suffix(maxLines))
    }
}

/// In-app log viewer: monospaced, newest last, filter by source, category and
/// text; refreshes every 2 seconds while open.
struct DiagnosticsLogView: View {
    @Environment(\.dismiss) private var dismiss
    @State private var source = "all"
    @State private var category = "all"
    @State private var search = ""
    @State private var lines: [DiagnosticsLogLine] = []
    @State private var followTail = true
    private let ticker = Timer.publish(every: 2, on: .main, in: .common).autoconnect()

    private var categories: [String] {
        ["all"] + Array(Set(lines.map(\.category))).sorted()
    }

    private var shown: [DiagnosticsLogLine] {
        let needle = search.trimmingCharacters(in: .whitespaces)
        return lines.filter { line in
            (category == "all" || line.category == category) &&
            (needle.isEmpty || line.text.localizedCaseInsensitiveContains(needle))
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Picker("Log", selection: $source) {
                    Text("All").tag("all")
                    Text("Agent").tag("agent")
                    Text("Diagnostics").tag("diagnostics")
                    Text("App").tag("app")
                }
                .pickerStyle(.segmented)
                .frame(width: 320)
                Picker("Category", selection: $category) {
                    ForEach(categories, id: \.self) { Text($0).tag($0) }
                }
                .frame(width: 200)
                TextField("Search", text: $search)
                    .textFieldStyle(.roundedBorder)
                Toggle("Follow", isOn: $followTail).toggleStyle(.checkbox)
            }
            ScrollViewReader { proxy in
                ScrollView([.vertical, .horizontal]) {
                    LazyVStack(alignment: .leading, spacing: 1) {
                        ForEach(shown) { line in
                            Text(line.text)
                                .font(.system(size: 11, design: .monospaced))
                                .foregroundStyle(color(line.level))
                                .textSelection(.enabled)
                                .fixedSize(horizontal: true, vertical: false)
                        }
                        Color.clear.frame(height: 1).id("bottom")
                    }
                    .padding(6)
                }
                .background(Color(nsColor: .textBackgroundColor))
                .border(Color.secondary.opacity(0.3))
                .onChange(of: lines.count) {
                    if followTail { proxy.scrollTo("bottom", anchor: .bottom) }
                }
                .onChange(of: category) { proxy.scrollTo("bottom", anchor: .bottom) }
            }
            HStack {
                Text("\(shown.count) of \(lines.count) lines · \(AppDiagnostics.logDirectory.path)")
                    .font(.caption).foregroundStyle(.secondary).textSelection(.enabled)
                Spacer()
                Button("Close") { dismiss() }.keyboardShortcut(.cancelAction)
            }
        }
        .padding(12)
        .frame(minWidth: 820, minHeight: 500)
        .task(id: source) { await reload() }
        .onReceive(ticker) { _ in Task { await reload() } }
    }

    private func reload() async {
        // "All" is agent + app: while diagnostic mode is on, diagnostics.log
        // repeats the agent's records, so merging it too would show each twice.
        var sources: [(name: String, url: URL)] = []
        if source == "all" || source == "agent" { sources.append(("agent", AppDiagnostics.agentLogURL)) }
        if source == "diagnostics" { sources.append(("diagnostics", AppDiagnostics.diagnosticsLogURL)) }
        if source == "all" || source == "app" { sources.append(("app", AppDiagnostics.appLogURL)) }
        let loaded = await Task.detached(priority: .utility) { DiagnosticsLogReader.load(sources) }.value
        lines = loaded
    }

    private func color(_ level: String) -> Color {
        switch level.uppercased() {
        case "ERROR": return .red
        case "WARN", "WARNING": return .orange
        case "DEBUG": return .secondary
        default: return .primary
        }
    }
}
