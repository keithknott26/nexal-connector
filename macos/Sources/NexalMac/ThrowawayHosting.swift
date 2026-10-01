import AppKit
import Foundation
import SwiftUI

// Throwaway hosts, Mac-app side (WP-D).
//
// Files shared with the Go connector (all under ~/Library/Application Support/Nexal):
//   sandbox-hosting.json                  written here, read by the connector: {enabled, maxSandboxes, placement}
//                                         placement is "members" (any network member) or "owner" (only my devices).
//   sandboxes/state.json                  written by the connector (sandbox.Manager), read here (JSON array of records).
//   sandboxes/kill-requests/<sandboxId>   written here to ask the connector to tear a host down now
//                                         (the connector calls Manager.Kill(id) and deletes the file).
// The connector has no local HTTP API for List/Kill, so files are the simplest consistent seam.
// Hosts on the network (other Macs, cloud) and Connect go through the connector CLI (`nexal sandbox ...`),
// because this app deliberately has no coordinator client.

/// `sandbox-hosting.json`.
struct SandboxHostingConfig: Codable, Equatable {
    var enabled = false
    var maxSandboxes = 5
    var placement = "members"

    static let placements: [(value: String, label: String)] = [
        ("members", "Any member of my network"), ("owner", "Only my own devices")]

    var clamped: SandboxHostingConfig {
        var c = self
        c.maxSandboxes = min(10, max(1, c.maxSandboxes))
        if !Self.placements.contains(where: { $0.value == c.placement }) { c.placement = "members" }
        return c
    }
}

/// One record of the connector's `sandboxes/state.json` (see sandbox.record). Everything but the id is optional.
struct LocalSandbox: Decodable, Identifiable, Equatable {
    let id: String
    var state: String?
    var hostname: String?
    var meshIp: String?
    var expiresAt: String?
    var kind: String?
    var lifecycle: String?
    var paused: Bool?

    var expiry: Date? { ThrowawayFormat.date(expiresAt) }
    /// Holds CPU, memory or disk (provisioning, running, stopping, or paused by sleep).
    var isActive: Bool { ["provisioning", "running", "stopping", "paused"].contains(state ?? "") }
    var isRunning: Bool { state == "running" || state == "paused" }
}

/// A host on the network, from `nexal sandbox --action list`.
struct NetworkSandbox: Decodable, Identifiable, Equatable {
    let id: String
    var name: String?
    var hostname: String?
    var kind: String?
    var lifecycle: String?
    var state: String?
    var meshIp: String?
    var expiresAt: String?
    var hostName: String?

    var title: String { name ?? hostname ?? id }
    var canConnect: Bool { state == "running" }
}

private struct NetworkSandboxList: Decodable { let sandboxes: [NetworkSandbox]? }

/// Reply of `nexal sandbox --action connect` (the coordinator's connect response, passed through).
struct SandboxConnectReply: Decodable {
    struct SFTP: Decodable { let host: String?; let port: Int?; let user: String?
        let certificate: String?; let hostKey: String? }
    let kind: String?
    let host: String?
    let port: Int?
    let user: String?
    let certificate: String?
    let hostKey: String?
    let password: String?
    let pending: Bool?
    let sftp: SFTP?
}

enum ThrowawayFormat {
    static func date(_ value: String?) -> Date? {
        guard let value else { return nil }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.date(from: value) ?? ISO8601DateFormatter().date(from: value)
    }

    static func countdown(to date: Date, now: Date = Date()) -> String {
        let s = Int(date.timeIntervalSince(now))
        if s <= 0 { return "expiring" }
        if s >= 3600 { return "\(s / 3600) h \((s % 3600) / 60) min left" }
        if s >= 60 { return "\(s / 60) min left" }
        return "\(s) s left"
    }

    static func kindLabel(_ kind: String?) -> String { kind == "devcontainer" ? "Dev container" : "Virtual machine" }
    static func lifecycleLabel(_ l: String?) -> String { l == "persistent" ? "Persistent" : "Temporary" }

    static func stateLabel(_ s: LocalSandbox) -> String {
        if s.paused == true || s.state == "paused" { return "Paused (Mac asleep)" }
        switch s.state ?? "" {
        case "provisioning": return "Starting"
        case "running": return "Running"
        case "stopping": return "Stopping"
        case "failed": return "Failed"
        case "deleted": return "Deleted"
        default: return "Unknown"
        }
    }

    static func runningLine(_ n: Int) -> String { "\(n) throwaway host\(n == 1 ? "" : "s") running" }
}

enum ThrowawayConnectError: LocalizedError {
    case unusable(String)
    var errorDescription: String? {
        switch self { case .unusable(let s): return s }
    }
}

@MainActor
final class ThrowawayHosting: ObservableObject {
    static let shared = ThrowawayHosting()

    @Published private(set) var config = SandboxHostingConfig()
    @Published private(set) var local: [LocalSandbox] = []
    @Published private(set) var network: [NetworkSandbox] = []
    @Published private(set) var message: String?
    @Published private(set) var busyID: String?
    private var lastNetworkFetch = Date.distantPast

    private let root: URL
    init(root: URL = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/Nexal", isDirectory: true)) {
        self.root = root
        reloadConfig()
        reloadLocal()
    }

    var configURL: URL { root.appendingPathComponent("sandbox-hosting.json") }
    var stateURL: URL { root.appendingPathComponent("sandboxes/state.json") }
    var killDir: URL { root.appendingPathComponent("sandboxes/kill-requests", isDirectory: true) }

    var runningHere: [LocalSandbox] { local.filter { $0.isActive } }
    var runningLine: String { ThrowawayFormat.runningLine(runningHere.count) }

    // MARK: Settings file

    func reloadConfig() {
        if let data = try? Data(contentsOf: configURL),
           let c = try? JSONDecoder().decode(SandboxHostingConfig.self, from: data) {
            config = c.clamped
        }
    }

    func update(_ change: (inout SandboxHostingConfig) -> Void) {
        var next = config
        change(&next)
        next = next.clamped
        guard next != config else { return }
        do {
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            let encoder = JSONEncoder()
            encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
            try encoder.encode(next).write(to: configURL, options: .atomic)
            try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: configURL.path)
            config = next
            message = nil
        } catch {
            message = "Could not save throwaway-host settings: \(error.localizedDescription)"
        }
    }

    // MARK: Container runtime (dev containers)

    @Published private(set) var runtimeStatus: String?
    @Published private(set) var installingRuntime = false

    /// Runs the bundled installer (Colima plus the docker and devpod tools, via
    /// Homebrew). The connector refuses Docker Desktop (`docker_desktop_only`), so
    /// dev containers need Colima, Lima, Podman or OrbStack. Idempotent.
    func installContainerRuntime() {
        guard !installingRuntime else { return }
        guard let script = Bundle.main.url(forResource: "install-container-runtime", withExtension: "sh") else {
            runtimeStatus = "This build does not include the container-runtime installer."
            return
        }
        installingRuntime = true
        runtimeStatus = "Setting up a container runtime for dev containers…"
        Task { @MainActor in
            let status = await Task.detached { ThrowawayHosting.runInstaller(script) }.value
            self.installingRuntime = false
            self.runtimeStatus = status
        }
    }

    nonisolated static func runInstaller(_ script: URL) -> String {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/bash")
        process.arguments = [script.path]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        do { try process.run() } catch {
            return "Could not start the container-runtime installer: \(error.localizedDescription)"
        }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        let lines = String(decoding: data, as: UTF8.self).split(whereSeparator: \.isNewline)
        if let last = lines.last { return String(last) }
        return process.terminationStatus == 0 ? "Container runtime ready." : "Container runtime setup failed."
    }

    // MARK: Hosts on this Mac

    func reloadLocal() {
        guard let data = try? Data(contentsOf: stateURL),
              let records = try? JSONDecoder().decode([LocalSandbox].self, from: data) else {
            local = []
            return
        }
        // Valid instances only: deleted and failed records stay in state.json for the connector but are not shown.
        local = records.filter { $0.state != "deleted" && $0.state != "failed" }
    }

    static func validID(_ id: String) -> Bool {
        !id.isEmpty && id.count <= 64
            && id.range(of: "^[A-Za-z0-9_-]+$", options: .regularExpression) != nil
    }

    /// Asks the connector to tear this host down now. The connector deletes the request once it acts on it.
    func stop(_ id: String) {
        guard Self.validID(id) else { return }
        do {
            try FileManager.default.createDirectory(at: killDir, withIntermediateDirectories: true)
            let file = killDir.appendingPathComponent(id)
            try Data().write(to: file, options: .atomic)
            message = "Stopping \(id)… The disk is erased once it has shut down."
        } catch {
            message = "Could not ask neXal@home to stop this host: \(error.localizedDescription)"
        }
    }

    func stopAll() { for s in runningHere { stop(s.id) } }

    // MARK: Hosts on the network and Connect (through the connector CLI)

    func refreshNetwork(_ model: AppModel, force: Bool = false) async {
        guard force || Date().timeIntervalSince(lastNetworkFetch) > 30 else { return }
        lastNetworkFetch = Date()
        // An older connector without the `sandbox` command simply yields no list.
        guard let data = try? await model.sandbox(action: "list", id: nil, kind: nil),
              let list = try? JSONDecoder().decode(NetworkSandboxList.self, from: data) else { return }
        network = (list.sandboxes ?? []).filter { $0.state != "deleted" && $0.state != "failed" }
    }

    func connect(_ box: NetworkSandbox, kind: String, model: AppModel) async {
        guard busyID == nil, Self.validID(box.id) else { return }
        busyID = box.id
        message = nil
        defer { busyID = nil }
        do {
            switch kind {
            case "vnc":
                let data = try await model.sandbox(action: "connect", id: box.id, kind: "vnc")
                let reply = try JSONDecoder().decode(SandboxConnectReply.self, from: data)
                if reply.pending == true {
                    throw ThrowawayConnectError.unusable("The host is still preparing its screen-sharing password. Try again in a few seconds.")
                }
                try Self.openScreenSharing(reply)
            default:
                let isFiles = kind == "files"
                let pair = try await Task.detached(priority: .userInitiated) { try Self.makeKeypair() }.value
                let data = try await model.sandbox(action: "connect", id: box.id, kind: kind,
                                                   input: Data((pair.publicKey + "\n").utf8))
                let reply = try JSONDecoder().decode(SandboxConnectReply.self, from: data)
                try Self.openTerminal(reply: reply, directory: pair.directory, files: isFiles)
            }
        } catch {
            message = error.localizedDescription
        }
    }

    // MARK: Connect helpers (pure enough to unit test)

    nonisolated static func validHost(_ s: String) -> Bool {
        !s.isEmpty && s.count <= 253 && s.range(of: "^[A-Za-z0-9.:-]+$", options: .regularExpression) != nil
    }
    nonisolated static func validUser(_ s: String) -> Bool {
        s.range(of: "^[a-z_][a-z0-9_-]{0,31}$", options: .regularExpression) != nil
    }
    /// One line of OpenSSH key material: no quotes, newlines or shell metacharacters.
    nonisolated static func validKeyLine(_ s: String) -> Bool {
        !s.isEmpty && s.count <= 8192 && s.range(of: "^[A-Za-z0-9@.+/=_ -]+$", options: .regularExpression) != nil
    }

    nonisolated static func knownHostsLine(host: String, port: Int, hostKey: String) -> String {
        let parts = hostKey.split(separator: " ").prefix(2).joined(separator: " ")
        return (port == 22 ? host : "[\(host)]:\(port)") + " " + parts + "\n"
    }

    nonisolated static func makeKeypair() throws -> (directory: URL, publicKey: String) {
        let fm = FileManager.default
        // Remove leftovers from connections that were never opened.
        let tmp = fm.temporaryDirectory
        if let old = try? fm.contentsOfDirectory(at: tmp, includingPropertiesForKeys: [.creationDateKey]) {
            for url in old where url.lastPathComponent.hasPrefix("nexal-connect-") {
                let created = (try? url.resourceValues(forKeys: [.creationDateKey]).creationDate) ?? Date()
                if Date().timeIntervalSince(created) > 3600 { try? fm.removeItem(at: url) }
            }
        }
        let dir = tmp.appendingPathComponent("nexal-connect-" + UUID().uuidString, isDirectory: true)
        try fm.createDirectory(at: dir, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/ssh-keygen")
        process.arguments = ["-q", "-t", "ed25519", "-N", "", "-C", "nexal-connect", "-f", dir.appendingPathComponent("key").path]
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        try process.run()
        process.waitUntilExit()
        guard process.terminationStatus == 0,
              let pub = try? String(contentsOf: dir.appendingPathComponent("key.pub"), encoding: .utf8) else {
            try? fm.removeItem(at: dir)
            throw ThrowawayConnectError.unusable("Could not create a temporary key for this connection.")
        }
        return (dir, pub.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    /// Writes the certificate and pinned host key beside the temporary key, then opens a Terminal window
    /// running ssh (or sftp for files). The script deletes the directory, and with it the key, when it ends.
    static func openTerminal(reply: SandboxConnectReply, directory: URL, files: Bool) throws {
        let access = files ? (reply.sftp ?? SandboxConnectReply.SFTP(host: reply.host, port: reply.port, user: reply.user,
                                                                      certificate: reply.certificate, hostKey: reply.hostKey))
                           : SandboxConnectReply.SFTP(host: reply.host, port: reply.port, user: reply.user,
                                                      certificate: reply.certificate, hostKey: reply.hostKey)
        guard let host = access.host, validHost(host), let user = access.user, validUser(user),
              let cert = access.certificate, validKeyLine(cert),
              let hostKey = access.hostKey, validKeyLine(hostKey) else {
            try? FileManager.default.removeItem(at: directory)
            throw ThrowawayConnectError.unusable("The host returned connection details this app cannot use.")
        }
        let port = access.port ?? 22
        guard (1...65535).contains(port) else {
            try? FileManager.default.removeItem(at: directory)
            throw ThrowawayConnectError.unusable("The host returned connection details this app cannot use.")
        }
        let fm = FileManager.default
        try (cert + "\n").write(to: directory.appendingPathComponent("key-cert.pub"), atomically: true, encoding: .utf8)
        try knownHostsLine(host: host, port: port, hostKey: hostKey)
            .write(to: directory.appendingPathComponent("known_hosts"), atomically: true, encoding: .utf8)
        let d = directory.path
        let tool = files ? "sftp -P" : "ssh -p"
        let script = """
        #!/bin/bash
        D='\(d)'
        trap 'rm -rf "$D"' EXIT
        clear
        \(tool) \(port) -i "$D/key" -o CertificateFile="$D/key-cert.pub" -o IdentitiesOnly=yes \
        -o UserKnownHostsFile="$D/known_hosts" -o StrictHostKeyChecking=yes \(user)@\(host)
        """
        let scriptURL = directory.appendingPathComponent(files ? "files.command" : "ssh.command")
        try script.write(to: scriptURL, atomically: true, encoding: .utf8)
        try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: scriptURL.path)
        guard NSWorkspace.shared.open(scriptURL) else {
            try? fm.removeItem(at: directory)
            throw ThrowawayConnectError.unusable("Could not open Terminal.")
        }
    }

    static func openScreenSharing(_ reply: SandboxConnectReply) throws {
        guard let host = reply.host, validHost(host), let password = reply.password, !password.isEmpty else {
            throw ThrowawayConnectError.unusable("The host returned connection details this app cannot use.")
        }
        var c = URLComponents()
        c.scheme = "vnc"
        c.user = "nexal"
        c.password = password
        c.host = host
        if let port = reply.port, port != 5900 { c.port = port }
        guard let url = c.url, NSWorkspace.shared.open(url) else {
            throw ThrowawayConnectError.unusable("Could not open Screen Sharing.")
        }
    }
}

// MARK: - Settings

/// Owner-only opt-in. OFF by default: nothing runs on this Mac until the owner turns it on.
struct ThrowawayHostingSettingsView: View {
    @ObservedObject private var hosting = ThrowawayHosting.shared
    @EnvironmentObject private var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Toggle("Allow this Mac to run throwaway hosts", isOn: Binding(
                get: { hosting.config.enabled },
                set: { on in
                    hosting.update { $0.enabled = on }
                    if on { hosting.installContainerRuntime() }
                }))
                .disabled(!model.isLinked)
            Text(model.isLinked
                 ? "Throwaway hosts are disposable virtual machines and dev containers that you or members of your network start. Only you, as this Mac's owner, can turn this on. Off by default."
                 : "Connect this Mac to neXal first. Only its owner can turn this on.")
                .font(.caption).foregroundStyle(.secondary)
            if hosting.config.enabled {
                Stepper(value: Binding(get: { hosting.config.maxSandboxes },
                                       set: { n in hosting.update { $0.maxSandboxes = n } }), in: 1...10) {
                    Text("Most hosts at once: \(hosting.config.maxSandboxes)")
                }
                Picker("Who can start one here", selection: Binding(
                    get: { hosting.config.placement },
                    set: { p in hosting.update { $0.placement = p } })) {
                    ForEach(SandboxHostingConfig.placements, id: \.value) { Text($0.label).tag($0.value) }
                }
                Text("Hosts use at most half of this Mac's processor and memory in total, always leave room on the disk, and are not started while this Mac is on battery. Each host's disk is erased when it is removed.")
                    .font(.caption).foregroundStyle(.secondary)
                Text("Hosts are polite to you: they pause when this Mac sleeps and resume when it wakes. You can stop any host from the menu bar or the neXal panel at any time.")
                    .font(.caption).foregroundStyle(.secondary)
                HStack(spacing: 8) {
                    Button(hosting.installingRuntime ? "Setting up…" : "Set up dev-container runtime") {
                        hosting.installContainerRuntime()
                    }
                    .disabled(hosting.installingRuntime)
                    if hosting.installingRuntime { ProgressView().controlSize(.small) }
                }
                Text(hosting.runtimeStatus ?? "Dev containers need Colima or OrbStack (Docker Desktop is not supported). Setup installs Colima, docker and devpod with Homebrew.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            if let message = hosting.message { Text(message).font(.caption).foregroundStyle(.orange) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .onAppear { hosting.reloadConfig() }
    }
}

// MARK: - Panel section

struct ThrowawayHostsSection: View {
    @ObservedObject private var hosting = ThrowawayHosting.shared
    @EnvironmentObject private var model: AppModel

    private var visible: Bool { hosting.config.enabled || !hosting.local.isEmpty || !hosting.network.isEmpty }

    var body: some View {
        Group {
            if visible {
                Divider()
                DisclosureGroup {
                    VStack(alignment: .leading, spacing: 8) {
                        if !hosting.local.isEmpty {
                            Text("ON THIS MAC").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
                            ForEach(hosting.local) { localRow($0) }
                        } else if hosting.config.enabled {
                            Text("No throwaway hosts are running on this Mac.").font(.caption).foregroundStyle(.secondary)
                        }
                        if !hosting.network.isEmpty {
                            Text("ON YOUR NETWORK").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
                            ForEach(hosting.network) { networkRow($0) }
                        }
                        if hosting.config.enabled {
                            Text("Hosts pause when this Mac sleeps and resume when it wakes.")
                                .font(.caption2).foregroundStyle(.secondary)
                        }
                        if let message = hosting.message {
                            Text(message).font(.caption2).foregroundStyle(.orange)
                        }
                    }.padding(.top, 4)
                } label: {
                    Text(hosting.runningHere.isEmpty ? "Throwaway hosts" : "Throwaway hosts · \(hosting.runningLine)")
                        .font(.subheadline.weight(.semibold))
                }
            }
        }
        .task {
            while !Task.isCancelled {
                hosting.reloadLocal()
                await hosting.refreshNetwork(model)
                try? await Task.sleep(nanoseconds: 5_000_000_000)
            }
        }
    }

    private func badge(_ text: String) -> some View {
        Text(text).font(.caption2)
            .padding(.horizontal, 6).padding(.vertical, 1)
            .background(.secondary.opacity(0.15), in: Capsule())
    }

    private func localRow(_ box: LocalSandbox) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Text(box.hostname ?? box.id).fontWeight(.medium)
                badge(ThrowawayFormat.kindLabel(box.kind))
                badge(ThrowawayFormat.lifecycleLabel(box.lifecycle))
                Spacer()
                if box.isActive {
                    Button(box.state == "stopping" ? "Stopping…" : "Stop", role: .destructive) { hosting.stop(box.id) }
                        .disabled(box.state == "stopping")
                }
            }
            HStack(spacing: 8) {
                Text(ThrowawayFormat.stateLabel(box)).foregroundStyle(.secondary)
                if let expiry = box.expiry, box.lifecycle != "persistent" {
                    TimelineView(.periodic(from: .now, by: 1)) { context in
                        Text(ThrowawayFormat.countdown(to: expiry, now: context.date)).foregroundStyle(.secondary)
                    }
                }
                if let ip = box.meshIp { Text(ip).foregroundStyle(.secondary).textSelection(.enabled) }
            }.font(.caption)
        }
    }

    private func networkRow(_ box: NetworkSandbox) -> some View {
        HStack(spacing: 6) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(box.title).fontWeight(.medium)
                    badge(ThrowawayFormat.kindLabel(box.kind))
                    badge(ThrowawayFormat.lifecycleLabel(box.lifecycle))
                }
                Text((box.state ?? "").capitalized).font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if box.canConnect {
                Menu("Connect") {
                    Button("Terminal (SSH)") { Task { await hosting.connect(box, kind: "ssh", model: model) } }
                    Button("Screen Sharing") { Task { await hosting.connect(box, kind: "vnc", model: model) } }
                    Button("Files (SFTP in Terminal)") { Task { await hosting.connect(box, kind: "files", model: model) } }
                }
                .disabled(hosting.busyID != nil)
                .fixedSize()
            }
        }
    }
}
