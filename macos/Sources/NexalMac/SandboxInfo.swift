import SwiftUI
import AppKit

/// One labelled value in the details sheet.
struct SandboxInfoRow: Equatable, Identifiable {
    let label: String
    let value: String
    /// Offers a copy button (addresses, commands, URLs).
    var copyable = false
    var id: String { label }
}

struct SandboxInfoSection: Equatable, Identifiable {
    let title: String
    let rows: [SandboxInfoRow]
    var id: String { title }
}

/// What the ⓘ sheet shows for one VM or development container. Built from the coordinator's record
/// (`NetworkSandbox`) and, when this Mac hosts it, the local record (`LocalSandbox`). Pure, so it is unit-tested.
/// Secrets are never shown: the Screen Sharing password is one-time and issued on Connect.
struct SandboxInfo: Equatable, Identifiable {
    var id: String { title }
    var title: String
    var stateText: String
    var sections: [SandboxInfoSection]
    /// The VM's console log on this Mac, when this Mac hosts it. nil hides "View logs".
    var logURL: URL? = nil
}

/// Reads the end of a VM's console log for the "View logs" pane.
enum SandboxLogReader {
    static let maxBytes = 96 * 1024

    /// The last `maxBytes` of the file, cleaned for display. nil when the file does not exist or cannot be read.
    static func tail(of url: URL, maxBytes: Int = SandboxLogReader.maxBytes) -> String? {
        guard let handle = try? FileHandle(forReadingFrom: url) else { return nil }
        defer { try? handle.close() }
        guard let size = try? handle.seekToEnd() else { return nil }
        let start = size > UInt64(maxBytes) ? size - UInt64(maxBytes) : 0
        guard (try? handle.seek(toOffset: start)) != nil, let data = try? handle.readToEnd() else { return nil }
        var text = String(decoding: data, as: UTF8.self)
        // Started mid-file: the first line is usually cut, so drop it.
        if start > 0, let newline = text.firstIndex(of: "\n") { text = String(text[text.index(after: newline)...]) }
        return clean(text)
    }

    /// Console output carries colour codes and carriage returns that show as noise in a text view.
    static func clean(_ raw: String) -> String {
        var s = raw.replacingOccurrences(of: "\r\n", with: "\n")
        s = s.replacingOccurrences(of: "\u{1B}\\[[0-9;?]*[A-Za-z]", with: "", options: .regularExpression)
        return s.filter { $0 == "\n" || $0 == "\t" || !($0.unicodeScalars.first.map { $0.value < 0x20 || $0.value == 0x7F } ?? false) }
    }
}

enum SandboxInfoBuilder {
    static let user = "nexal"

    static func make(network: NetworkSandbox?, local: LocalSandbox?, title: String, logURL: URL? = nil, now: Date = Date()) -> SandboxInfo {
        let kind = network?.kind ?? local?.kind
        let isContainer = kind == "devcontainer"
        let lifecycle = network?.lifecycle ?? local?.lifecycle
        let meshIp = clean(network?.meshIp) ?? clean(local?.meshIp)
        let lanIp = clean(local?.lanIp) ?? clean(network?.lanIp)
        let hostname = clean(network?.hostname) ?? clean(local?.hostname)
        let rawState = local?.paused == true ? "paused" : (local?.state ?? network?.state)
        let app = InstanceApp(profile: network?.appProfile)

        var status: [SandboxInfoRow] = [.init(label: "State", value: stateLabel(rawState))]
        if let failure = clean(network?.failure) { status.append(.init(label: "Problem", value: failure)) }
        status.append(.init(label: "Type", value: [ThrowawayFormat.kindLabel(kind), ThrowawayFormat.lifecycleLabel(lifecycle)].joined(separator: ", ")))
        if lifecycle != "persistent", let expiry = ThrowawayFormat.date(network?.expiresAt ?? local?.expiresAt) {
            status.append(.init(label: "Expires", value: ThrowawayFormat.countdown(to: expiry, now: now).replacingOccurrences(of: "Deleted in", with: "In")))
        }
        if let host = clean(network?.runnerName) {
            status.append(.init(label: "Runs on", value: host + (network?.runnerOnline == false ? " (offline)" : "")))
        } else if local != nil {
            status.append(.init(label: "Runs on", value: "This Mac"))
        }

        var network_: [SandboxInfoRow] = []
        if let meshIp { network_.append(.init(label: "Private network IP", value: meshIp, copyable: true)) }
        if let lanIp { network_.append(.init(label: "Home network IP", value: lanIp, copyable: true)) }
        if meshIp == nil && lanIp == nil { network_.append(.init(label: "Address", value: "Not assigned yet")) }
        if let hostname { network_.append(.init(label: "Host name", value: hostname, copyable: true)) }
        if let reach = clean(network?.reach) {
            network_.append(.init(label: "Reach", value: reach == "isolated" ? "Only you" : "Everyone on this network"))
        }

        var access: [SandboxInfoRow] = [.init(label: "User name", value: user, copyable: true),
                                        .init(label: "Sign-in", value: "Your neXal key, no password. Use Connect › Terminal.")]
        if let meshIp { access.append(.init(label: "SSH command", value: "ssh \(user)@\(meshIp)", copyable: true)) }
        if !isContainer {
            access.append(.init(label: "Screen Sharing", value: network?.desktop == false
                ? "No desktop in this VM"
                : "One-time password, issued on Connect"))
        }
        if let app, let meshIp {
            access.append(.init(label: app.title, value: "http://\(meshIp):\(app.port)", copyable: true))
            if let lanIp { access.append(.init(label: "\(app.title) (home)", value: "http://\(lanIp):\(app.port)", copyable: true)) }
        }

        var system: [SandboxInfoRow] = []
        if let image = imageText(network) { system.append(.init(label: isContainer ? "Template" : "Image", value: image)) }
        if let app { system.append(.init(label: "App", value: app.title)) }
        if let cpu = network?.cpu, let mem = network?.memoryMb, let disk = network?.diskGb {
            let size = [network?.size.map { $0.capitalized }, "\(cpu) CPU, \(mem >= 1024 ? "\(mem / 1024) GB" : "\(mem) MB") memory, \(disk) GB disk"]
                .compactMap { $0 }
            system.append(.init(label: "Size", value: size.count == 2 ? "\(size[0]): \(size[1])" : size[0]))
        }
        if let drive = clean(network?.drive) { system.append(.init(label: "Shared drive", value: drive.capitalized)) }

        var activity: [SandboxInfoRow] = []
        if let created = ThrowawayFormat.date(network?.createdAt) { activity.append(.init(label: "Created", value: created.formatted(date: .abbreviated, time: .shortened))) }
        if let by = clean(network?.lastConnectedByName) {
            let at = ThrowawayFormat.date(network?.lastConnectedAt).map { " · " + $0.formatted(date: .abbreviated, time: .shortened) } ?? ""
            activity.append(.init(label: "Last connected by", value: by + at))
        }

        let sections = [SandboxInfoSection(title: "Status", rows: status),
                        SandboxInfoSection(title: "Network", rows: network_),
                        SandboxInfoSection(title: "Login", rows: access),
                        SandboxInfoSection(title: "System", rows: system),
                        SandboxInfoSection(title: "Activity", rows: activity)]
            .filter { !$0.rows.isEmpty }
        return SandboxInfo(title: title, stateText: stateLabel(rawState), sections: sections, logURL: isContainer ? nil : logURL)
    }

    private static func clean(_ s: String?) -> String? {
        guard let s = s?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty else { return nil }
        return s
    }

    private static func imageText(_ box: NetworkSandbox?) -> String? {
        guard let box else { return nil }
        if box.kind == "devcontainer", let t = box.devcontainer?.template, !t.isEmpty { return t.prefix(1).uppercased() + t.dropFirst() }
        guard let name = box.imageName else { return nil }
        return [name, box.imageVersion].compactMap { $0 }.joined(separator: " ")
    }

    static func stateLabel(_ state: String?) -> String {
        switch state ?? "" {
        case "requested": return "Queued"
        case "provisioning": return "Starting"
        case "running": return "Running"
        case "stopping": return "Stopping"
        case "stopped": return "Stopped"
        case "paused": return "Paused (Mac asleep)"
        case "failed": return "Failed"
        case "": return "Unknown"
        default: return state!.capitalized
        }
    }
}

/// A small modal with the details of one instance. Presented with `.sheet(item:)` (popovers and alerts are
/// unreliable inside the menu-bar panel).
struct SandboxInfoSheet: View {
    let info: SandboxInfo
    @Environment(\.dismiss) private var dismiss
    @State private var copied: String?
    @State private var showingLogs = false

    var body: some View {
        if showingLogs, let url = info.logURL {
            SandboxLogView(title: info.title, url: url) { showingLogs = false }
        } else {
            details
        }
    }

    private var details: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Image(systemName: "info.circle").foregroundStyle(.secondary)
                Text(info.title).font(.headline).lineLimit(1).truncationMode(.middle)
                Spacer()
                Text(info.stateText).font(.caption.weight(.medium)).foregroundStyle(.secondary)
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    ForEach(info.sections) { section in
                        VStack(alignment: .leading, spacing: 4) {
                            Text(section.title.uppercased()).font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
                            ForEach(section.rows) { row in
                                HStack(alignment: .firstTextBaseline, spacing: 8) {
                                    Text(row.label).font(.caption).foregroundStyle(.secondary).frame(width: 118, alignment: .leading)
                                    Text(row.value).font(row.copyable ? .system(.caption, design: .monospaced) : .caption).textSelection(.enabled)
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                    if row.copyable {
                                        Button { copy(row) } label: { Image(systemName: copied == row.id ? "checkmark" : "doc.on.doc") }
                                            .buttonStyle(.borderless).controlSize(.small).help("Copy")
                                    }
                                }
                            }
                        }
                    }
                }
            }
            HStack {
                if info.logURL != nil {
                    Button { showingLogs = true } label: { Label("View logs", systemImage: "doc.text.magnifyingglass") }
                        .help("The VM's console output: boot, mesh join, app install")
                }
                Spacer()
                Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
            }
        }
        .padding(16)
        .frame(width: 340, height: 420)
    }

    private func copy(_ row: SandboxInfoRow) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(row.value, forType: .string)
        copied = row.id
    }
}

/// The ⓘ button on an instance card.
struct SandboxInfoButton: View {
    let action: () -> Void
    var body: some View {
        Button(action: action) { Image(systemName: "info.circle") }
            .buttonStyle(.borderless)
            .help("Details")
    }
}

/// The console log of a VM hosted on this Mac, refreshed while it is open, so a starting, running or
/// stopping VM can be followed live. Shown inside the details sheet (nested sheets are unreliable in the panel).
struct SandboxLogView: View {
    let title: String
    let url: URL
    let back: () -> Void
    @State private var text: String?
    @State private var loaded = false
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Button(action: back) { Label("Details", systemImage: "chevron.left") }.buttonStyle(.borderless)
                Spacer()
                Text("Logs · \(title)").font(.caption.weight(.medium)).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
            ScrollViewReader { proxy in
                ScrollView {
                    Text(display)
                        .font(.system(size: 10, design: .monospaced))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Color.clear.frame(height: 1).id("end")
                }
                .background(RoundedRectangle(cornerRadius: 6).fill(.quaternary.opacity(0.35)))
                .onChange(of: text) { proxy.scrollTo("end", anchor: .bottom) }
            }
            HStack {
                Button {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(text ?? "", forType: .string)
                    copied = true
                } label: { Label(copied ? "Copied" : "Copy", systemImage: copied ? "checkmark" : "doc.on.doc") }
                    .disabled(text == nil)
                Button { NSWorkspace.shared.activateFileViewerSelecting([url]) } label: { Label("Show in Finder", systemImage: "folder") }
                    .disabled(text == nil)
                Spacer()
                Text("Updates every 2 s").font(.caption2).foregroundStyle(.secondary)
            }
            .controlSize(.small)
        }
        .padding(16)
        .frame(width: 340, height: 420)
        .task {
            while !Task.isCancelled {
                let url = self.url
                let fresh = await Task.detached { SandboxLogReader.tail(of: url) }.value
                if fresh != text { text = fresh }
                loaded = true
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    private var display: String {
        guard loaded else { return "Loading…" }
        guard let text else { return "No log yet. It appears once the VM starts." }
        return text.isEmpty ? "The log is empty." : text
    }
}

/// A mesh peer that is one of the network's VMs or containers (matched by its private-network address).
/// Such a peer is not a Mac: it has no account named like this Mac's user and no keys of its own, so
/// its Connect buttons must go through the sandbox connect flow (user `nexal`, one-time credentials).
enum SandboxPeerMatch {
    static func sandbox(forMeshIp ip: String?, in list: [NetworkSandbox]) -> NetworkSandbox? {
        guard let ip = ip?.trimmingCharacters(in: .whitespaces), !ip.isEmpty else { return nil }
        return list.first { $0.meshIp == ip && $0.state != "deleted" }
    }
}
