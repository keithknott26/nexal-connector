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
    var failure: String?

    var title: String { name ?? hostname ?? id }
    var canConnect: Bool { state == "running" }
}

private struct NetworkSandboxList: Decodable { let sandboxes: [NetworkSandbox]? }

struct SandboxImage: Decodable, Identifiable, Equatable {
    let id: String
    let name: String
    let version: String?
    let level: String
    let minDiskGb: Int?
    let checksumPending: Bool?
}
struct SandboxImageCatalog: Decodable {
    struct Runner: Decodable { let id: String; let name: String; let hostingEnabled: Bool? }
    let runner: Runner
    let images: [SandboxImage]
}
struct SandboxCreateRequest: Encodable {
    let imageId: String
    let runnerHostId: String
    let size: String
    let kind: String
    let lifecycle: String
    let lifetimeHours: Int?
    let reach: String
    enum CodingKeys: String, CodingKey { case imageId, runnerHostId, size, kind, lifecycle, lifetimeHours, reach }
    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(imageId, forKey: .imageId); try c.encode(runnerHostId, forKey: .runnerHostId)
        try c.encode(size, forKey: .size); try c.encode(kind, forKey: .kind); try c.encode(lifecycle, forKey: .lifecycle)
        try c.encode(reach, forKey: .reach)
        if lifecycle != "persistent", let lifetimeHours { try c.encode(lifetimeHours, forKey: .lifetimeHours) }
    }
}

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

    static func runningLine(_ n: Int) -> String { "\(n) running" }
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
        // Keep the runtime present and running on a Mac that hosts throwaway hosts
        // (after an update, a reboot, or a first install that had no Homebrew).
        // The installer exits at once when a runtime already answers.
        if config.enabled { installContainerRuntime() }
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
            message = "Could not save Virtual Machine Hosting settings: \(error.localizedDescription)"
        }
    }

    // MARK: Container runtime (dev containers)

    @Published private(set) var runtimeStatus: String?
    @Published private(set) var installingRuntime = false

    /// Runs the bundled installer: Colima, Lima, docker and devpod, from Homebrew when
    /// it is installed and otherwise downloaded by neXal (checksum-verified) into
    /// ~/Library/Application Support/Nexal/runtime. The connector refuses Docker
    /// Desktop (`docker_desktop_only`). Idempotent.
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
        let all = list.sandboxes ?? []
        network = all.filter { $0.state != "deleted" && $0.state != "failed" }
        allFailed = all.filter { $0.state == "failed" }
        applyDismissedFailures()
    }

    // Failures stay on the coordinator (a failed host holds no resources); Clear hides them
    // in this app. Remembered by id so they stay hidden after a relaunch.
    private static let dismissedKey = "dismissedSandboxFailures"
    private var allFailed: [NetworkSandbox] = []
    private var dismissedFailures: Set<String> {
        get { Set(UserDefaults.standard.stringArray(forKey: Self.dismissedKey) ?? []) }
        set { UserDefaults.standard.set(Array(newValue.suffix(200)), forKey: Self.dismissedKey) }
    }
    private func applyDismissedFailures() {
        let hidden = dismissedFailures
        failed = Array(allFailed.filter { !hidden.contains($0.id) }.prefix(3))
    }
    func clearFailures() {
        dismissedFailures = dismissedFailures.union(allFailed.map(\.id))
        applyDismissedFailures()
    }

    // MARK: Create (VM or dev container, on this Mac)

    @Published private(set) var failed: [NetworkSandbox] = []

    /// Network hosts not already listed under "On this Mac" (the coordinator also reports
    /// hosts running here, so the same host would otherwise show twice).
    var networkElsewhere: [NetworkSandbox] {
        var here = Set<String>()
        for box in local {
            here.insert(box.id.lowercased())
            if let name = box.hostname { here.insert(name.lowercased()) }
        }
        return network.filter { box in
            let keys: [String?] = [box.id, box.hostname, box.name]
            return !keys.contains { key in key.map { value in here.contains(value.lowercased()) } ?? false }
        }
    }
    @Published var showingCreate = false
    @Published private(set) var creating = false

    /// The image catalog for this Mac (the connector asks for its own host by default).
    func loadImages(_ model: AppModel) async throws -> SandboxImageCatalog {
        let data = try await model.sandbox(action: "images", id: nil, kind: nil)
        return try JSONDecoder().decode(SandboxImageCatalog.self, from: data)
    }

    func create(_ request: SandboxCreateRequest, model: AppModel) async -> Bool {
        guard !creating else { return false }
        creating = true; message = nil
        defer { creating = false }
        do {
            let body = try JSONEncoder().encode(request)
            _ = try await model.sandbox(action: "create", id: nil, kind: nil, input: body)
            await refreshNetwork(model, force: true)
            return true
        } catch {
            message = "Could not create the instance: \(error.localizedDescription)"
            return false
        }
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
            Toggle("Allow this Mac to host virtual machines and dev containers", isOn: Binding(
                get: { hosting.config.enabled },
                set: { on in
                    hosting.update { $0.enabled = on }
                    if on { hosting.installContainerRuntime() }
                }))
                .disabled(!model.isLinked)
            Text(model.isLinked
                 ? "Virtual machines and dev containers are disposable Linux machines that you or members of your network start. Only you, as this Mac's owner, can turn this on. Off by default."
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
                Text(hosting.runtimeStatus ?? "Dev containers need a container runtime (Docker Desktop is not supported). neXal@home installs Colima, docker and devpod for you; no Homebrew needed.")
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

    private var visible: Bool { true }

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
                            Text("No virtual machines or dev containers are running on this Mac.").font(.caption).foregroundStyle(.secondary)
                        }
                        if !hosting.networkElsewhere.isEmpty {
                            Text("ON YOUR NETWORK").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
                            ForEach(hosting.networkElsewhere) { networkRow($0) }
                        }
                        if hosting.config.enabled {
                            Text("Hosts pause when this Mac sleeps and resume when it wakes.")
                                .font(.caption2).foregroundStyle(.secondary)
                        }
                        if !hosting.failed.isEmpty {
                            HStack {
                                Text("RECENT FAILURES").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
                                Spacer()
                                Button("Clear") { hosting.clearFailures() }
                                    .buttonStyle(.borderless).controlSize(.small).font(.caption2)
                                    .help("Remove these failures from the list")
                            }
                            ForEach(hosting.failed) { box in
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(box.title).font(.caption.weight(.medium))
                                    Text(box.failure ?? "Failed").font(.caption2).foregroundStyle(.red).textSelection(.enabled).lineLimit(4)
                                }
                            }
                        }
                        if let message = hosting.message {
                            Text(message).font(.caption2).foregroundStyle(.orange)
                        }
                        Button { hosting.showingCreate = true } label: {
                            Label("New virtual machine or dev container…", systemImage: "plus.circle.fill")
                        }
                        .disabled(hosting.creating)
                    }.padding(.top, 4)
                } label: {
                    Text(hosting.runningHere.isEmpty ? "Virtual Machines & Development Containers" : "Virtual Machines & Development Containers · \(hosting.runningLine)")
                        .font(.subheadline.weight(.semibold))
                }
            }
        }
        .sheet(isPresented: $hosting.showingCreate) { NewSandboxSheet().environmentObject(model) }
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


// MARK: - Create sheet

struct NewSandboxSheet: View {
    @ObservedObject private var hosting = ThrowawayHosting.shared
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss
    @State private var kind = "devcontainer"
    @State private var size = "small"
    @State private var lifecycle = "ephemeral"
    @State private var lifetime = 24
    @State private var reach = "network"
    @State private var catalog: SandboxImageCatalog?
    @State private var imageId = ""
    @State private var loadError: String?

    private var images: [SandboxImage] { (catalog?.images ?? []).filter { kind == "devcontainer" || $0.checksumPending != true } }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("New virtual machine or dev container").font(.headline)
            Form {
                Picker("Type", selection: $kind) {
                    Text("Dev container").tag("devcontainer")
                    Text("Virtual machine").tag("vm")
                }.pickerStyle(.segmented)
                Picker("Image", selection: $imageId) {
                    ForEach(images) { image in Text(image.version.map { "\(image.name) \($0)" } ?? image.name).tag(image.id) }
                }.disabled(images.isEmpty)
                Picker("Size", selection: $size) {
                    Text("Small · 2 CPU, 2 GB").tag("small")
                    Text("Medium · 4 CPU, 8 GB").tag("medium")
                    Text("Large · 8 CPU, 16 GB").tag("large")
                }
                Picker("Keep", selection: $lifecycle) {
                    Text("Temporary").tag("ephemeral")
                    Text("Persistent").tag("persistent")
                }
                if lifecycle == "ephemeral" {
                    Picker("Delete after", selection: $lifetime) {
                        Text("1 hour").tag(1); Text("4 hours").tag(4); Text("1 day").tag(24)
                        Text("3 days").tag(72); Text("1 week").tag(168)
                    }
                }
                Picker("Can reach", selection: $reach) {
                    Text("My computers").tag("network")
                    Text("Nothing (isolated)").tag("isolated")
                }
            }
            .formStyle(.grouped)
            if let loadError { Text(loadError).font(.caption).foregroundStyle(.red) }
            if catalog?.runner.hostingEnabled == false {
                Text("This Mac isn't set up to host instances. Turn on hosting in Settings › Virtual Machine Hosting.").font(.caption).foregroundStyle(.orange)
            }
            Text(kind == "devcontainer"
                 ? "Runs in Colima on this Mac and joins your private network. The first one downloads the runtime and can take a few minutes."
                 : "A full Linux VM on this Mac that joins your private network.")
                .font(.caption).foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(hosting.creating ? "Creating…" : "Create") { Task { await create() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(hosting.creating || imageId.isEmpty || catalog == nil)
            }
        }
        .padding(20)
        .frame(width: 460)
        .task { await load() }
        .onChange(of: kind) { _, _ in if !images.contains(where: { $0.id == imageId }) { imageId = images.first?.id ?? "" } }
    }

    private func load() async {
        do {
            let c = try await hosting.loadImages(model)
            catalog = c
            imageId = images.first?.id ?? ""
            if c.images.isEmpty { loadError = "No compatible images for this Mac yet." }
        } catch {
            loadError = "Could not load images: \(error.localizedDescription)"
        }
    }

    private func create() async {
        guard let catalog else { return }
        let request = SandboxCreateRequest(imageId: imageId, runnerHostId: catalog.runner.id, size: size, kind: kind,
                                           lifecycle: lifecycle, lifetimeHours: lifecycle == "ephemeral" ? lifetime : nil, reach: reach)
        if await hosting.create(request, model: model) { dismiss() }
    }
}
