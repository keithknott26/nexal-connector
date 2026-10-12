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
}

enum SandboxInfoBuilder {
    static let user = "nexal"

    static func make(network: NetworkSandbox?, local: LocalSandbox?, title: String, now: Date = Date()) -> SandboxInfo {
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
        return SandboxInfo(title: title, stateText: stateLabel(rawState), sections: sections)
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

    var body: some View {
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
            HStack { Spacer(); Button("Done") { dismiss() }.keyboardShortcut(.defaultAction) }
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
