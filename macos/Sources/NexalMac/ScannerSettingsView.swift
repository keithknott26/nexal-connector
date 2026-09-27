import SwiftUI
import AppKit

struct ScannerReply: Decodable {
    let enabled: Bool
    let status: String
    let roots: [String]?
    let enginePath: String?
    let engineVersion: String?
    let rulesVersion: String?
    let lastScanAt: String?
    let lastError: String?
    let filesScanned: Int?
    let filesSkipped: Int?
    let findings: Int?
    let pendingEvents: Int?
    let expiredEvents: Int?
    let baselineId: String?
    let coverage: String?
}

struct LocalScannerFindingsReply: Decodable { let findings: [LocalScannerFinding]; let truncated: Bool? }
struct LocalScannerFinding: Decodable, Identifiable {
    let eventId: String; let path: String; let ruleId: String; let engine: String
    let contentSha256: String; let observedAt: String; let severity: String; let testOnly: Bool
    let score: Double?; let baselineId: String?
    var id: String { eventId + ":" + path }
}

enum ScannerPresentation {
    static func rule(_ id: String) -> String {
        switch id {
        case "nexal_eicar_test": return "Harmless scanner self-test; not a malware finding."
        case "nexal_script_download_execute": return "Downloaded content piped into a shell. Legitimate installers also use this pattern."
        case "nexal_powershell_encoded_hidden": return "Encoded and hidden PowerShell options. These can be used for legitimate administration or malicious activity."
        case "nexal_python_reverse_shell": return "Socket and shell-redirection patterns. This does not prove a remote shell ran."
        case "nexal_macho_persistence_credentials": return "A Mac executable contains startup, persistence, credential, and shell-related strings. This combination does not prove those actions were executed."
        case "style_baseline_deviation": return "Formatting differs from an approved style baseline. This does not establish malware or AI authorship."
        default: return "Review this rule identifier and the file’s context before drawing a conclusion."
        }
    }
    static func status(_ value: String?) -> String {
        switch value {
        case "disabled": return "Off"
        case "idle": return "Idle"
        case "running": return "Scanning"
        case "limited": return "Limited coverage"
        case "error": return "Scan error"
        case "engine_unavailable": return "Scan engine unavailable"
        default: return "Not reported"
        }
    }
    static func error(_ value: String) -> String {
        switch value {
        case "none": return "No error reported"
        case "engine_unavailable": return "The scan engine is unavailable. Install the complete neXal app package or select an approved engine in Advanced."
        case "engine_failed": return "The scan engine could not complete its check. Review the engine setup and try again."
        case "scan_limit": return "The scan reached a safety limit. Some files were skipped; choose smaller folders for more complete coverage."
        case "state_error": return "The scanner could not save or read its local state. Check local storage and permissions."
        case "delivery_pending": return "Findings are saved locally and waiting to reach your dashboard."
        default: return "The scanner reported an unknown error. Refresh its status before continuing."
        }
    }
    static func date(_ value: String?) -> String {
        guard let value else { return "Not reported" }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: value) { return date.formatted(date: .abbreviated, time: .shortened) }
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: value)?.formatted(date: .abbreviated, time: .shortened) ?? "Not reported"
    }
}

/// Local opt-in. Merely opening this panel neither scans files nor approves a baseline.
struct ScannerSettingsView: View {
    @EnvironmentObject private var model: AppModel
    @State private var state: ScannerReply?
    @State private var roots: [String] = []
    @State private var engine: String?
    @State private var busy = false
    @State private var error: String?
    @State private var confirmBaseline = false
    @State private var findings: [LocalScannerFinding]?
    @State private var findingsTruncated = false
    @State private var findingsBusy = false
    @State private var findingsError: String?

    var body: some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: 10) {
                Text("Choose folders to inspect without running their files. YARA-X checks byte patterns; code-style analysis compares supported scripts with a baseline you explicitly trust. Neither check continuously monitors running apps or proves AI authorship.")
                    .font(.caption).foregroundStyle(.secondary)
                ForEach(roots, id: \.self) { path in
                    HStack {
                        Text(path).font(.caption).lineLimit(2).textSelection(.enabled)
                        Spacer()
                        Button { roots.removeAll { $0 == path } } label: { Image(systemName: "minus.circle") }
                            .accessibilityLabel("Remove folder \(path)").disabled(busy)
                    }
                }
                HStack {
                    Button("Choose folders…") { chooseFolders() }.disabled(busy)
                    Button(state?.enabled == true ? "Save folders" : "Enable scanning") {
                        Task { await perform(.securityConfigure(roots: roots, engine: engine, enabled: true)) }
                    }.disabled(busy || state == nil || roots.isEmpty)
                }
                Text("Folder changes apply when you save. At least one folder is required; pausing preserves the saved folders. neXal’s private state is automatically excluded, even when you choose your home folder. File paths stay on this Mac.")
                    .font(.caption2).foregroundStyle(.secondary)
                if let state {
                    LabeledContent("Last reported state", value: ScannerPresentation.status(state.status))
                    LabeledContent("Last completed pass", value: ScannerPresentation.date(state.lastScanAt))
                    LabeledContent("Scope", value: state.coverage == "configured_roots" ? "Configured folders only" : "Not reported")
                    LabeledContent("Rules", value: state.rulesVersion ?? "Not reported")
                    Text("Last pass · \(state.filesScanned.map(String.init) ?? "Unknown") scanned · \(state.filesSkipped.map(String.init) ?? "Unknown") skipped · \(state.findings.map(String.init) ?? "Unknown") findings")
                        .font(.caption)
                    if let pending = state.pendingEvents, pending > 0 { Text("\(pending) findings waiting to be delivered").font(.caption).foregroundStyle(.orange) }
                    if let expired = state.expiredEvents, expired > 0 {
                        Text("\(expired) older findings retained locally; no longer uploadable").font(.caption).foregroundStyle(.secondary)
                    }
                    if let lastError = state.lastError, lastError != "none" { Text(ScannerPresentation.error(lastError)).font(.caption).foregroundStyle(.orange) }
                    HStack {
                        Button("Scan now") { Task { await perform(.securityScan) } }.disabled(busy || !state.enabled)
                        if state.enabled { Button("Pause scanning") { Task { await perform(.securityConfigure(roots: [], engine: nil, enabled: false)) } }.disabled(busy) }
                        Button("Refresh") { Task { await perform(.securityStatus) } }.disabled(busy)
                    }
                    Divider()
                    Text(state.baselineId == nil ? "No approved code-style baseline" : "Code-style baseline approved locally").font(.caption)
                    Button("Approve current scripts as baseline…") { confirmBaseline = true }.disabled(busy || (state.roots ?? []).isEmpty)
                    Text("Uses the saved folders. Approve only scripts you have reviewed and trust. At least three supported scripts of the same type with 20 nonblank lines each are needed. A style difference is not a threat probability.")
                        .font(.caption2).foregroundStyle(.secondary)
                }
                DisclosureGroup("Recent local findings") {
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Up to 100 recent findings from this Mac. Paths stay local. No file is opened or executed by this view.").font(.caption2).foregroundStyle(.secondary)
                        Button(findingsBusy ? "Loading…" : "Refresh local findings") { Task { await loadFindings() } }.disabled(busy || findingsBusy)
                        if findingsTruncated { Text("Only the newest findings that fit this view are shown. Additional records remain in the local ledger.").font(.caption2).foregroundStyle(.secondary) }
                        if findings?.isEmpty == true { Text("No local findings recorded. This does not prove every file was scanned.").font(.caption) }
                        ForEach(findings ?? []) { finding in
                            VStack(alignment: .leading, spacing: 4) {
                                Text(URL(fileURLWithPath: finding.path).lastPathComponent).font(.caption.weight(.semibold))
                                Text(finding.path).font(.caption2).textSelection(.enabled)
                                Text(ScannerPresentation.rule(finding.ruleId)).font(.caption).fixedSize(horizontal: false, vertical: true)
                                Text("\(finding.testOnly ? "Test finding" : finding.engine == "nexal_style" ? "Informational style signal" : finding.severity.capitalized) · \(ScannerPresentation.date(finding.observedAt))").font(.caption2)
                                DisclosureGroup("Technical evidence") {
                                    Text("Rule: \(finding.ruleId)").font(.caption2).textSelection(.enabled)
                                    Text("Content SHA-256: \(finding.contentSha256)").font(.caption2).textSelection(.enabled)
                                    if let score = finding.score, finding.engine == "nexal_style", score.isFinite, (0...1).contains(score) {
                                        Text("Style difference: \(score * 100, specifier: "%.1f")% — not a threat probability").font(.caption2)
                                    }
                                }
                            }.padding(.vertical, 5)
                            Divider()
                        }
                        if let findingsError { Text(findingsError).font(.caption).foregroundStyle(.orange) }
                    }
                }
                DisclosureGroup("Advanced engine settings") {
                    Text(engine ?? state?.enginePath ?? "Use the bundled scan engine").font(.caption).textSelection(.enabled)
                    Text("Engine version: \(state?.engineVersion ?? "Not reported")").font(.caption2)
                    Button("Choose engine…") { chooseEngine() }.disabled(busy)
                    Text("The complete app uses its bundled engine by default. An override is a local executable you trust; it applies when you save folders or enable scanning.")
                        .font(.caption2).foregroundStyle(.secondary)
                }
                if busy { HStack { ProgressView().controlSize(.small); Text("Working… A scan can take up to two minutes.").font(.caption) } }
                if let error {
                    Text(error).font(.caption).foregroundStyle(.orange).textSelection(.enabled)
                    if state == nil { Button("Retry scanner status") { Task { await perform(.securityStatus) } }.disabled(busy) }
                }
            }.font(.caption).padding(.top, 8)
        } label: { Label("File scanning & code-style review", systemImage: "doc.text.magnifyingglass").font(.subheadline.weight(.semibold)) }
        .fixedSize(horizontal: false, vertical: true)
        .task { await perform(.securityStatus) }
        .alert("Trust the current scripts?", isPresented: $confirmBaseline) {
            Button("Cancel", role: .cancel) {}
            Button("Approve baseline") { Task { await perform(.securityBaseline) } }
        } message: {
            Text("This records code-style measurements from the scripts in your saved folders as an approved baseline. Review those scripts first. Approval does not certify them as malware-free, identify their author, or authorize automatic mitigation.")
        }
    }
    private func chooseFolders() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true; panel.canChooseFiles = false; panel.allowsMultipleSelection = true
        panel.prompt = "Add folders"
        panel.begin { result in
            guard result == .OK else { return }
            roots = Array(Set(roots + panel.urls.map(\.path))).sorted()
        }
    }
    private func chooseEngine() {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = false; panel.canChooseFiles = true; panel.allowsMultipleSelection = false
        panel.prompt = "Choose trusted engine"
        panel.begin { result in if result == .OK { engine = panel.url?.path } }
    }
    private func loadFindings() async {
        guard !findingsBusy else { return }
        findingsBusy = true; defer { findingsBusy = false }
        do { let reply = try await model.scannerFindings(); findings = Array(reply.findings.prefix(100)); findingsTruncated = reply.truncated == true; findingsError = nil }
        catch { findingsError = error.localizedDescription }
    }
    private func perform(_ command: CLICommand) async {
        guard !busy else { return }
        busy = true; defer { busy = false }
        do {
            let reply = try await model.scanner(command)
            state = reply; roots = reply.roots ?? []; engine = nil; error = nil
            if case .securityScan = command { await loadFindings() }
        } catch { self.error = error.localizedDescription }
    }
}
