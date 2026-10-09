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
//   sandboxes/stop-requests/<sandboxId>   written here to shut a PERSISTENT VM down but keep its disk (state "stopped").
//   sandboxes/start-requests/<sandboxId>  written here to boot a stopped persistent VM again from its disk.
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
    /// The VM's address on this Mac's home network (bridged by nexal-vmnet), if any.
    var lanIp: String?
    var expiresAt: String?
    var kind: String?
    var lifecycle: String?
    var paused: Bool?

    var expiry: Date? { ThrowawayFormat.date(expiresAt) }
    /// Holds CPU, memory or disk (provisioning, running, stopping, or paused by sleep).
    var isActive: Bool { ["provisioning", "running", "stopping", "paused"].contains(state ?? "") }
    var isRunning: Bool { state == "running" || state == "paused" }
    var isStopped: Bool { state == "stopped" }
    /// Only a persistent VM can be stopped without deleting it (ephemeral ones are wiped on every stop).
    var canStopKeepingDisk: Bool { lifecycle == "persistent" && kind != "devcontainer" }
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
    var imageId: String?
    var imageName: String?
    var imageVersion: String?
    var appProfile: String?
    /// False when the VM was created without a desktop; nil from older coordinators.
    var desktop: Bool?
    var runnerName: String?
    var runnerOnline: Bool?
    var devcontainer: DevcontainerInfo?
    var progress: Progress?

    struct DevcontainerInfo: Decodable, Equatable { var template: String? }
    struct Progress: Decodable, Equatable { var step: String; var percent: Int? }

    var title: String { name ?? hostname ?? id }
    var canConnect: Bool { state == "running" }
    /// The OS or app family, for its icon: the dev container's template, else the image.
    var family: String { InstanceFamily.of(imageId: imageId, imageName: imageName, appProfile: appProfile, template: devcontainer?.template) }
}

private struct NetworkSandboxList: Decodable { let sandboxes: [NetworkSandbox]? }

struct SandboxImage: Decodable, Identifiable, Equatable {
    let id: String
    let name: String
    let version: String?
    let level: String
    let minDiskGb: Int?
    let checksumPending: Bool?
    let family: String?
    let appProfile: String?
    /// "vm" and/or "devcontainer"; absent from older coordinators.
    let kinds: [String]?

    /// Dev-container images are containers only; appliance images (Home Assistant) VMs only.
    func supports(_ kind: String) -> Bool {
        if let kinds { return kinds.contains(kind) }
        if family == "devcontainer" { return kind == "devcontainer" }
        if let appProfile, appProfile != "none" { return kind == "vm" }
        return true
    }
    var iconFamily: String { InstanceFamily.of(imageId: id, imageName: name, appProfile: appProfile, template: nil) }
}
struct SandboxImageCatalog: Decodable {
    struct Runner: Decodable { let id: String; let name: String; let hostingEnabled: Bool?; let containersOnly: Bool? }
    let runner: Runner
    let images: [SandboxImage]
}
/// Where an instance can be created (`nexal sandbox --action runners`).
struct SandboxRunner: Decodable, Identifiable, Equatable {
    let id: String
    let name: String
    let thisComputer: Bool?
    let managed: Bool?
    let containersOnly: Bool?
    let locked: Bool?
    let lockedReason: String?
}
private struct SandboxRunnerList: Decodable { let runners: [SandboxRunner]? }
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
        if s <= 0 { return "Being deleted" }
        if s >= 86_400 { return "Deleted in \(s / 86_400) d \((s % 86_400) / 3600) h" }
        if s >= 3600 { return "Deleted in \(s / 3600) h \((s % 3600) / 60) min" }
        if s >= 60 { return "Deleted in \(s / 60) min" }
        return "Deleted in \(s) s"
    }

    static func kindLabel(_ kind: String?) -> String { kind == "devcontainer" ? "Development container" : "Virtual machine" }
    static func lifecycleLabel(_ l: String?) -> String { l == "persistent" ? "Persistent" : "Temporary" }

    static func stateLabel(_ s: LocalSandbox) -> String {
        if s.paused == true || s.state == "paused" { return "Paused (Mac asleep)" }
        switch s.state ?? "" {
        case "provisioning": return "Starting"
        case "running": return "Running"
        case "stopping": return "Stopping"
        case "stopped": return "Stopped"
        case "failed": return "Failed"
        case "deleted": return "Deleted"
        default: return "Unknown"
        }
    }

    static func runningLine(_ n: Int) -> String { "\(n) running" }
}

enum ThrowawayConnectError: LocalizedError {
    case unusable(String)
    /// The guest is still setting the one-time screen-sharing password; asking again in a few seconds works.
    case pending
    var errorDescription: String? {
        switch self {
        case .unusable(let s): return s
        case .pending: return "The host is still preparing its screen-sharing password. Try again in a few seconds."
        }
    }
}

@MainActor
final class ThrowawayHosting: ObservableObject {
    static let shared = ThrowawayHosting()

    @Published private(set) var config = SandboxHostingConfig()
    @Published private(set) var local: [LocalSandbox] = []

    /// The home-network address of a VM hosted on this Mac, by its mesh address.
    func lanAddress(forMesh meshIP: String?) -> String? {
        guard let meshIP else { return nil }
        return local.first { $0.meshIp == meshIP }?.lanIp
    }
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
    var stopDir: URL { root.appendingPathComponent("sandboxes/stop-requests", isDirectory: true) }
    var startDir: URL { root.appendingPathComponent("sandboxes/start-requests", isDirectory: true) }

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

    /// nil until the first check finishes: neither a green nor a red LED is
    /// honest before then. After that, true/false come straight from the
    /// installer's exit code (0 ready, 1 failure) -- never guessed from its text.
    @Published private(set) var runtimeReady: Bool?
    @Published private(set) var runtimeStatus: String?
    @Published private(set) var installingRuntime = false

    /// Runs the bundled installer: Colima, Lima, docker and devpod, from Homebrew when
    /// it is installed and otherwise downloaded by neXal (checksum-verified) into
    /// ~/Library/Application Support/Nexal/runtime. The connector refuses Docker
    /// Desktop (`docker_desktop_only`). Idempotent: when a runtime already answers,
    /// this is just the readiness check (the whole point of calling it again), not
    /// a reinstall -- that is also why it is safe to run once, silently, whenever
    /// dev-container hosting is enabled, rather than only when the owner clicks it.
    func installContainerRuntime() {
        guard !installingRuntime else { return }
        guard let script = Bundle.main.url(forResource: "install-container-runtime", withExtension: "sh") else {
            runtimeReady = false
            runtimeStatus = "This build does not include the container-runtime installer."
            return
        }
        installingRuntime = true
        Task { @MainActor in
            let result = await Task.detached { ThrowawayHosting.runInstaller(script) }.value
            self.installingRuntime = false
            self.runtimeReady = result.ready
            self.runtimeStatus = result.message
        }
    }

    // MARK: Home-network bridge (nexal-vmnet)

    /// The root helper that gives VMs a second NIC on this Mac's LAN (vmnet bridged
    /// mode, socket_vmnet-style). nil until checked.
    @Published private(set) var lanBridgeReady: Bool?
    @Published private(set) var lanBridgeStatus: String?
    @Published private(set) var installingLANBridge = false
    nonisolated static let lanBridgeSocket = "/var/run/nexal-vmnet.sock"

    /// Running means its socket exists (the daemon creates it on start, owned by this user).
    func checkLANBridge() {
        var st = stat()
        let up = lstat(Self.lanBridgeSocket, &st) == 0 && (st.st_mode & S_IFMT) == S_IFSOCK
        lanBridgeReady = up
        if up { lanBridgeStatus = nil }
    }

    /// Installs (or reinstalls) the helper the app ships, as root through the
    /// standard macOS administrator prompt. It is copied root-owned out of the
    /// bundle, so a later change to the app cannot change what runs as root.
    func installLANBridge() {
        guard !installingLANBridge else { return }
        let helper = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/nexal-vmnet")
        guard let script = Bundle.main.url(forResource: "install-vmnet", withExtension: "sh"),
              FileManager.default.isExecutableFile(atPath: helper.path) else {
            lanBridgeReady = false
            lanBridgeStatus = "This build does not include the home-network bridge."
            return
        }
        installingLANBridge = true
        let uid = getuid()
        Task { @MainActor in
            let result = await Task.detached { ThrowawayHosting.runPrivileged(script: script, helper: helper, uid: uid) }.value
            self.installingLANBridge = false
            self.checkLANBridge()
            if self.lanBridgeReady != true { self.lanBridgeStatus = result }
        }
    }

    /// Runs install-vmnet.sh as root via the administrator prompt. Paths and the uid
    /// travel as AppleScript arguments (quoted form), never spliced into script text.
    nonisolated static func runPrivileged(script: URL, helper: URL, uid: uid_t) -> String {
        let source = [
            "on run argv",
            "do shell script \"/bin/bash \" & quoted form of item 1 of argv & \" --binary \" & quoted form of item 2 of argv & \" --uid \" & quoted form of item 3 of argv with prompt \"neXal@home wants to install its home-network bridge, so virtual machines can get an address on your home network.\" with administrator privileges",
            "end run",
        ]
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        process.arguments = source.flatMap { ["-e", $0] } + [script.path, helper.path, String(uid)]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        do { try process.run() } catch {
            return "Could not start the installer: \(error.localizedDescription)"
        }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        let text = String(decoding: data, as: UTF8.self)
        if text.contains("-128") { return "Setup was cancelled." }  // user pressed Cancel at the prompt
        let last = text.split(whereSeparator: \.isNewline).last.map(String.init)
        return process.terminationStatus == 0 ? (last ?? "Installed.") : (last ?? "The home-network bridge could not be installed.")
    }

    nonisolated static func runInstaller(_ script: URL) -> (ready: Bool, message: String) {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/bash")
        process.arguments = [script.path]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        do { try process.run() } catch {
            return (false, "Could not start the container-runtime installer: \(error.localizedDescription)")
        }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        let lines = String(decoding: data, as: UTF8.self).split(whereSeparator: \.isNewline)
        let ready = process.terminationStatus == 0
        let last = lines.last.map(String.init)
        let fallback = ready ? "Container runtime ready." : "Container runtime setup failed."
        return (ready, last ?? fallback)
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
            message = nil
        } catch {
            message = "Could not stop it: \(error.localizedDescription)"
        }
    }

    /// Removes an instance that runs on another computer of this network, through the coordinator. That
    /// computer's runner tears it down on its next poll, so the row shows "Stopping" until it does.
    func deleteRemote(_ id: String, model: AppModel) async {
        guard Self.validID(id), busyID == nil else { return }
        busyID = id
        message = nil
        defer { busyID = nil }
        do {
            _ = try await model.sandbox(action: "delete", id: id, kind: nil)
            await refreshNetwork(model, force: true)
        } catch {
            message = "Could not remove it: \(error.localizedDescription)"
        }
    }

    /// Stops (keeping its disk) or starts a persistent VM that runs on another computer, through the
    /// coordinator; that computer's runner carries it out on its next poll.
    func setRemotePower(_ id: String, start: Bool, model: AppModel) async {
        guard Self.validID(id), busyID == nil else { return }
        busyID = id
        message = nil
        defer { busyID = nil }
        do {
            _ = try await model.sandbox(action: start ? "start" : "stop", id: id, kind: nil)
            await refreshNetwork(model, force: true)
        } catch {
            message = "Could not \(start ? "start" : "stop") it: \(error.localizedDescription)"
        }
    }

    /// Asks the connector to shut a persistent VM down and keep its disk, or (`start`) to boot it again.
    func requestKeepingDisk(_ id: String, start: Bool) {
        guard Self.validID(id) else { return }
        let dir = start ? startDir : stopDir
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            try Data().write(to: dir.appendingPathComponent(id), options: .atomic)
            message = nil
        } catch {
            message = "Could not \(start ? "start" : "stop") it: \(error.localizedDescription)"
        }
    }

    /// Quit-time "stop all": persistent VMs are stopped with their disks kept; everything else
    /// (ephemeral VMs, dev containers) keeps the old behavior and is torn down.
    func stopAll() {
        for s in runningHere {
            if s.canStopKeepingDisk && s.isRunning { requestKeepingDisk(s.id, start: false) }
            else if s.canStopKeepingDisk { continue } // provisioning or already stopping: never erase a persistent disk from here
            else { stop(s.id) }
        }
    }

    // MARK: Hosts on the network and Connect (through the connector CLI)

    func refreshNetwork(_ model: AppModel, force: Bool = false) async {
        // Every 4 s while something is starting or stopping, so the progress bar moves; else every 30 s.
        let busy = network.contains { InstanceProgress.shows($0.state) } || local.contains { $0.state == "provisioning" || $0.state == "stopping" }
        guard force || Date().timeIntervalSince(lastNetworkFetch) > (busy ? 4 : 30) else { return }
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

    /// The coordinator's entry for a host running on this Mac (Connect goes through it).
    func networkEntry(for box: LocalSandbox) -> NetworkSandbox? {
        let keys = Set([box.id.lowercased()] + (box.hostname.map { [$0.lowercased()] } ?? []))
        return network.first { entry in
            let candidates: [String?] = [entry.id, entry.hostname, entry.name]
            return candidates.contains { key in key.map { value in keys.contains(value.lowercased()) } ?? false }
        }
    }

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
    func loadImages(_ model: AppModel, runner: String? = nil) async throws -> SandboxImageCatalog {
        let data = try await model.sandbox(action: "images", id: runner, kind: nil)
        return try JSONDecoder().decode(SandboxImageCatalog.self, from: data)
    }

    /// Where an instance can be created; empty from an older connector or coordinator.
    func loadRunners(_ model: AppModel) async -> [SandboxRunner] {
        guard let data = try? await model.sandbox(action: "runners", id: nil, kind: nil),
              let list = try? JSONDecoder().decode(SandboxRunnerList.self, from: data) else { return [] }
        return list.runners ?? []
    }

    func clearMessage() { message = nil }

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
                    throw ThrowawayConnectError.pending
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

    /// The screen address and a fresh password for the built-in viewer (hover preview, live window).
    /// Unlike `connect`, this does not claim `busyID` or set `message`: a hover must not grey out the
    /// Connect menus. Every call asks the connector again, because the password is one-time; nothing is cached.
    func vncEndpoint(for box: NetworkSandbox, model: AppModel) async throws -> VNCEndpoint {
        guard Self.validID(box.id) else {
            throw ThrowawayConnectError.unusable("This host has no usable identifier.")
        }
        let data = try await model.sandbox(action: "connect", id: box.id, kind: "vnc")
        let reply = try JSONDecoder().decode(SandboxConnectReply.self, from: data)
        return try Self.vncEndpoint(from: reply)
    }

    // MARK: Connect helpers (pure enough to unit test)

    nonisolated static func vncEndpoint(from reply: SandboxConnectReply) throws -> VNCEndpoint {
        if reply.pending == true { throw ThrowawayConnectError.pending }
        let port = reply.port ?? 5900
        guard let host = reply.host, validHost(host), let password = reply.password, !password.isEmpty,
              (1...65535).contains(port) else {
            throw ThrowawayConnectError.unusable("The host returned connection details this app cannot use.")
        }
        return VNCEndpoint(host: host, port: port, password: password)
    }

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
        -o UserKnownHostsFile="$D/known_hosts" -o StrictHostKeyChecking=yes \
        -o ServerAliveInterval=15 -o ServerAliveCountMax=8 -o TCPKeepAlive=yes \(user)@\(host)
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
            Toggle("Allow this Mac to host virtual machines and development containers", isOn: Binding(
                get: { hosting.config.enabled },
                set: { on in
                    hosting.update { $0.enabled = on }
                    if on { hosting.installContainerRuntime() }
                }))
                .disabled(!model.isLinked)
            Text(model.isLinked
                 ? "Virtual machines and development containers are Linux machines that you or members of your network start. Only you, as this Mac's owner, can turn this on. Off by default."
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
                // The LED is the point of this row: dev containers need a container
                // runtime (Colima + docker + devpod; Docker Desktop is refused), and once
                // it is set up there is nothing left to click -- just confirmation it is
                // still there. Green/red only ever come from the installer's own exit
                // code (runtimeReady), never guessed from its wording.
                HStack(spacing: 6) {
                    if hosting.installingRuntime {
                        ProgressView().controlSize(.small)
                        Text("Checking the dev-container runtime…").font(.caption).foregroundStyle(.secondary)
                    } else {
                        Circle()
                            .fill(hosting.runtimeReady == true ? Color.green : hosting.runtimeReady == false ? Color.red : Color.secondary.opacity(0.4))
                            .frame(width: 8, height: 8)
                        Text(hosting.runtimeReady == true ? "Dev-container runtime ready"
                             : hosting.runtimeReady == false ? (hosting.runtimeStatus ?? "Dev-container runtime needs attention")
                             : "Dev-container runtime: not checked yet")
                            .font(.caption)
                            .foregroundStyle(hosting.runtimeReady == true ? Color.secondary : hosting.runtimeReady == false ? Color.red : Color.secondary)
                        Spacer()
                        Button(hosting.runtimeReady == true ? "Recheck" : "Set up") {
                            hosting.installContainerRuntime()
                        }
                        .controlSize(.small)
                    }
                }
                if hosting.runtimeReady != false {
                    Text("Dev containers need a container runtime (Docker Desktop is not supported). neXal@home installs Colima, docker and devpod for you; no Homebrew needed.")
                        .font(.caption).foregroundStyle(.secondary)
                }
                // Same LED pattern: green only when the helper's socket is really there.
                HStack(spacing: 6) {
                    if hosting.installingLANBridge {
                        ProgressView().controlSize(.small)
                        Text("Setting up the home-network bridge…").font(.caption).foregroundStyle(.secondary)
                    } else {
                        Circle()
                            .fill(hosting.lanBridgeReady == true ? Color.green : hosting.lanBridgeStatus != nil ? Color.red : Color.secondary.opacity(0.4))
                            .frame(width: 8, height: 8)
                        Text(hosting.lanBridgeReady == true ? "Home network: virtual machines get an address on your network"
                             : hosting.lanBridgeStatus ?? "Home network: not set up (virtual machines are reachable over neXal only)")
                            .font(.caption)
                            .foregroundStyle(hosting.lanBridgeReady != true && hosting.lanBridgeStatus != nil ? Color.red : Color.secondary)
                        Spacer()
                        Button(hosting.lanBridgeReady == true ? "Reinstall" : "Set up") { hosting.installLANBridge() }
                            .controlSize(.small)
                    }
                }
                Text("With the home-network bridge, new virtual machines also get an address from your router, so devices that are not on neXal (a TV, for example) can reach apps like Jellyfin. Setting it up asks for your password once.")
                    .font(.caption).foregroundStyle(.secondary)
                NetworkSetupCheckView(autoRun: hosting.lanBridgeReady == true && hosting.local.contains { $0.isRunning && $0.lanIp == nil })
            }
            if let message = hosting.message { Text(message).font(.caption).foregroundStyle(.orange) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .onAppear {
            hosting.reloadConfig()
            hosting.checkLANBridge()
        }
    }
}

// MARK: - Panel section

struct ThrowawayHostsSection: View {
    @ObservedObject private var hosting = ThrowawayHosting.shared
    @ObservedObject private var peerNames = PeerNames.shared
    @EnvironmentObject private var model: AppModel
    /// The instance whose stop/delete is awaiting confirmation.
    @State private var pendingStop: PendingStop?

    private struct PendingStop {
        let id: String
        let title: String
        let kind: String?
        /// True for an instance that runs on another computer (removed through the coordinator).
        var remote = false
        var isContainer: Bool { kind == "devcontainer" }
    }

    var body: some View {
        Group {
            DisclosureGroup {
                VStack(alignment: .leading, spacing: 10) {
                    if hosting.local.isEmpty && hosting.networkElsewhere.isEmpty {
                        emptyState
                    }
                    if !hosting.local.isEmpty {
                        sectionLabel("On this Mac")
                        ForEach(hosting.local) { box in localCard(box) }
                    }
                    if !hosting.networkElsewhere.isEmpty {
                        sectionLabel("On your other computers")
                        ForEach(hosting.networkElsewhere) { box in networkCard(box) }
                    }
                    if !hosting.failed.isEmpty { failures }
                    if let message = hosting.message {
                        Label(message, systemImage: "exclamationmark.triangle.fill")
                            .font(.caption).foregroundStyle(.orange).textSelection(.enabled)
                    }
                    HStack {
                        Button { hosting.showingCreate = true } label: {
                            Label("New…", systemImage: "plus")
                        }
                        .buttonStyle(.borderedProminent)
                        .controlSize(.small)
                        .disabled(hosting.creating)
                        .help("Create a virtual machine or development container")
                        Spacer()
                        if hosting.config.enabled {
                            Text("Paused while this Mac sleeps").font(.caption2).foregroundStyle(.tertiary)
                        }
                    }
                }
                .padding(.top, 6)
            } label: {
                HStack(spacing: 6) {
                    Text("Virtual Machines & Development Containers").font(.subheadline.weight(.semibold))
                    if !hosting.runningHere.isEmpty {
                        Text("\(hosting.runningHere.count)")
                            .font(.caption2.weight(.semibold).monospacedDigit())
                            .padding(.horizontal, 6).padding(.vertical, 1)
                            .background(Color.accentColor.opacity(0.2), in: Capsule())
                            .help(hosting.runningLine)
                    }
                }
            }
        }
        .sheet(isPresented: $hosting.showingCreate) { NewSandboxSheet().environmentObject(model) }
        // Attached to the panel the user clicked in. A modal NSAlert from this
        // menu-bar app opened behind the frontmost app's window: on macOS 14+ an
        // app can only ask to be activated, not force it.
        .alert(pendingStop.map { $0.isContainer ? "Stop and delete “\($0.title)”?" : "Shut down and remove “\($0.title)”?" } ?? "",
               isPresented: Binding(get: { pendingStop != nil }, set: { if !$0 { pendingStop = nil } }),
               presenting: pendingStop) { stop in
            Button(stop.isContainer ? "Stop and Delete" : "Shut Down and Remove", role: .destructive) { if stop.remote { Task { await hosting.deleteRemote(stop.id, model: model) } } else { hosting.stop(stop.id) } }
            Button("Cancel", role: .cancel) {}
        } message: { stop in
            Text(stop.isContainer
                 ? "The development container and everything in it are deleted. This cannot be undone."
                 : "The virtual machine shuts down and its disk is erased. This cannot be undone.")
        }
        .task {
            while !Task.isCancelled {
                hosting.reloadLocal()
                await hosting.refreshNetwork(model)
                try? await Task.sleep(nanoseconds: 4_000_000_000)
            }
        }
    }

    // MARK: Pieces

    private func sectionLabel(_ text: String) -> some View {
        Text(text.uppercased()).font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
    }

    private var emptyState: some View {
        HStack(spacing: 10) {
            Image(systemName: "square.stack.3d.up").font(.title3).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 2) {
                Text("Nothing running").font(.callout.weight(.medium))
                Text(hosting.config.enabled
                     ? "Create a Linux VM or a development container that joins your private network."
                     : "Turn on hosting in Settings › Virtual Machine Hosting to run them on this Mac, or create one on another computer.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(.quaternary.opacity(0.35)))
    }

    private var failures: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Label("Recent failures", systemImage: "exclamationmark.octagon.fill")
                    .font(.caption.weight(.semibold)).foregroundStyle(.red)
                Spacer()
                Button("Clear") { hosting.clearFailures() }
                    .buttonStyle(.borderless).controlSize(.small)
                    .help("Remove these failures from the list")
            }
            ForEach(hosting.failed) { box in
                VStack(alignment: .leading, spacing: 1) {
                    Text(PeerNames.shared.name(for: box.meshIp) ?? box.title).font(.caption.weight(.medium))
                    Text(box.failure ?? "Failed").font(.caption2).foregroundStyle(.secondary).textSelection(.enabled).lineLimit(4)
                }
            }
        }
        .padding(10)
        .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(Color.red.opacity(0.08)))
        .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Color.red.opacity(0.25)))
    }

    private func localCard(_ box: LocalSandbox) -> some View {
        let entry = hosting.networkEntry(for: box)
        let title = PeerNames.shared.name(for: box.meshIp) ?? entry?.name ?? box.hostname ?? box.id
        let state = box.paused == true ? "paused" : box.state
        return InstanceCard(
            title: title, address: box.meshIp,
            family: entry?.family ?? (box.kind == "devcontainer" ? "devcontainer" : "linux"),
            kind: box.kind, state: state,
            details: [Self.imageText(entry), ThrowawayFormat.kindLabel(box.kind), box.meshIp,
                      box.lanIp.map { "home network \($0)" }].compactMap { $0 },
            expiry: box.lifecycle == "persistent" ? nil : box.expiry,
            progressStep: entry?.progress?.step, progressPercent: entry?.progress?.percent,
            appTitle: InstanceApp(profile: entry?.appProfile)?.title, runnerName: nil, runnerOnline: true,
            preview: previewTarget(for: entry, title: title, kind: box.kind, state: state, paused: box.paused)
        ) {
            if let entry, entry.canConnect || box.state == "running" { connectMenu(entry, lan: box.lanIp) }
            if box.canStopKeepingDisk {
                if box.isStopped {
                    Button { hosting.requestKeepingDisk(box.id, start: true) } label: { Image(systemName: "play.circle") }
                        .buttonStyle(.borderless)
                        .help("Start")
                } else if box.isRunning {
                    Button { hosting.requestKeepingDisk(box.id, start: false) } label: { Image(systemName: "stop.circle") }
                        .buttonStyle(.borderless)
                        .help("Stop (keeps its disk)")
                }
                if box.isActive || box.isStopped {
                    Button { confirmStop(id: box.id, title: title, kind: box.kind) } label: { Image(systemName: "trash") }
                        .buttonStyle(.borderless)
                        .disabled(box.state == "stopping" || box.state == "provisioning")
                        .help("Delete (erases its disk)")
                }
            } else if box.isActive {
                Button { confirmStop(id: box.id, title: title, kind: box.kind) } label: {
                    Image(systemName: "stop.circle")
                }
                .buttonStyle(.borderless)
                .disabled(box.state == "stopping")
                .help(box.kind == "devcontainer" ? "Stop and delete" : "Shut down and remove")
            }
        }
    }

    private func networkCard(_ box: NetworkSandbox) -> some View {
        let title = PeerNames.shared.name(for: box.meshIp) ?? box.title
        return InstanceCard(
            title: title, address: box.meshIp, family: box.family, kind: box.kind, state: box.state,
            details: [Self.imageText(box), box.runnerName.map { "on \($0)" }, box.meshIp].compactMap { $0 },
            expiry: box.lifecycle == "persistent" ? nil : ThrowawayFormat.date(box.expiresAt),
            progressStep: box.progress?.step, progressPercent: box.progress?.percent,
            appTitle: InstanceApp(profile: box.appProfile)?.title, runnerName: box.runnerName, runnerOnline: box.runnerOnline,
            preview: previewTarget(for: box, title: title, kind: box.kind, state: box.state, paused: nil)
        ) {
            if box.canConnect { connectMenu(box) }
            if box.lifecycle == "persistent" && box.kind != "devcontainer" {
                if box.state == "stopped" {
                    Button { Task { await hosting.setRemotePower(box.id, start: true, model: model) } } label: { Image(systemName: "play.circle") }
                        .buttonStyle(.borderless)
                        .disabled(hosting.busyID == box.id)
                        .help("Start")
                } else if box.state == "running" {
                    Button { Task { await hosting.setRemotePower(box.id, start: false, model: model) } } label: { Image(systemName: "stop.circle") }
                        .buttonStyle(.borderless)
                        .disabled(hosting.busyID == box.id)
                        .help("Stop (keeps its disk)")
                }
            }
            if box.state != "stopping" && box.state != "deleted" {
                Button { confirmStop(id: box.id, title: title, kind: box.kind, remote: true) } label: { Image(systemName: "trash") }
                    .buttonStyle(.borderless)
                    .disabled(hosting.busyID == box.id)
                    .help(box.runnerOnline == false ? "Remove (it goes away when that computer is back online)" : "Remove")
            }
        }
    }

    /// Only a running virtual machine has a screen to show. Stopped, paused, starting and container rows get nil.
    private func previewTarget(for entry: NetworkSandbox?, title: String, kind: String?, state: String?, paused: Bool?) -> VMPreviewTarget? {
        guard let entry, ThrowawayHosting.validID(entry.id),
              VMPreviewEligibility.unavailableReason(kind: kind ?? entry.kind, state: state, paused: paused, desktop: entry.desktop) == nil else { return nil }
        let hosting = self.hosting
        let model = self.model
        return VMPreviewTarget(
            id: entry.id, title: title,
            fetchEndpoint: { try await hosting.vncEndpoint(for: entry, model: model) },
            openScreenSharing: { Task { await hosting.connect(entry, kind: "vnc", model: model) } })
    }

    private static func imageText(_ box: NetworkSandbox?) -> String? {
        guard let box else { return nil }
        if box.kind == "devcontainer", let template = box.devcontainer?.template, !template.isEmpty {
            return template.prefix(1).uppercased() + template.dropFirst()
        }
        guard let name = box.imageName else { return nil }
        return [name, box.imageVersion].compactMap { $0 }.joined(separator: " ")
    }

    private func confirmStop(id: String, title: String, kind: String?, remote: Bool = false) {
        pendingStop = PendingStop(id: id, title: title, kind: kind, remote: remote)
    }

    /// Dev containers have no screen, so they offer Terminal and Files only.
    /// `lan` is the VM's home-network address when it is hosted on this Mac.
    private func connectMenu(_ box: NetworkSandbox, lan: String? = nil) -> some View {
        Menu {
            if let app = InstanceApp(profile: box.appProfile), let ip = box.meshIp, let url = URL(string: "http://\(ip):\(app.port)") {
                Button { NSWorkspace.shared.open(url) } label: { Label("Open \(app.title)", systemImage: "safari") }
                if let lan, let lanURL = URL(string: "http://\(lan):\(app.port)") {
                    Button { NSWorkspace.shared.open(lanURL) } label: { Label("Open \(app.title) (home network)", systemImage: "house") }
                }
                Divider()
            }
            Button { Task { await hosting.connect(box, kind: "ssh", model: model) } } label: { Label("Terminal (SSH)", systemImage: "terminal") }
            if box.kind != "devcontainer" {
                if box.desktop != false { Button { Task { await hosting.connect(box, kind: "vnc", model: model) } } label: { Label("Screen Sharing", systemImage: "rectangle.on.rectangle") } }
            }
            Button { Task { await hosting.connect(box, kind: "files", model: model) } } label: { Label("Files (SFTP)", systemImage: "folder") }
        } label: {
            Text("Connect")
        }
        .menuStyle(.borderlessButton)
        .controlSize(.small)
        .disabled(hosting.busyID != nil)
        .fixedSize()
    }
}

/// Clicking the OS tile of a running VM opens its live window. (Not the title: a double-click there renames.)
private struct LiveViewTap: ViewModifier {
    let enabled: Bool
    let action: () -> Void
    func body(content: Content) -> some View {
        Group {
            if enabled {
                content.contentShape(Rectangle()).onTapGesture(perform: action)
            } else {
                content
            }
        }
    }
}

/// One VM or development container: OS tile, name, status, details, actions and progress.
struct InstanceCard<Actions: View>: View {
    let title: String
    let address: String?
    let family: String
    let kind: String?
    let state: String?
    let details: [String]
    let expiry: Date?
    let progressStep: String?
    let progressPercent: Int?
    var appTitle: String? = nil
    var runnerName: String? = nil
    var runnerOnline: Bool? = nil
    /// Set for a running virtual machine only: hovering the row shows a live thumbnail and clicking opens the live window.
    var preview: VMPreviewTarget? = nil
    @ViewBuilder let actions: () -> Actions

    @State private var previewModel: VMPreviewModel?
    @State private var hoverTask: Task<Void, Never>?

    /// Opens the thumbnail after the pointer has rested on the row for 0.4 s; closes it shortly after
    /// the pointer leaves the row and the thumbnail (the gap between them must not flicker it).
    private func hoverChanged(_ inside: Bool) {
        guard preview != nil else { return }
        hoverTask?.cancel()
        if inside {
            guard previewModel == nil else { return }
            hoverTask = Task { @MainActor in
                try? await Task.sleep(nanoseconds: 400_000_000)
                guard !Task.isCancelled, let preview, previewModel == nil else { return }
                let model = VMPreviewModel(mode: .preview, fetchEndpoint: preview.fetchEndpoint)
                previewModel = model
                model.start()
            }
        } else {
            hoverTask = Task { @MainActor in
                try? await Task.sleep(nanoseconds: 250_000_000)
                guard !Task.isCancelled else { return }
                closePreview()
            }
        }
    }

    private func closePreview() {
        hoverTask?.cancel()
        hoverTask = nil
        previewModel?.stop()
        previewModel = nil
    }

    private func openLive() {
        guard let preview else { return }
        closePreview()
        VMLiveWindowController.shared.show(preview)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .center, spacing: 10) {
                InstanceIcon(family: family, size: 30)
                    .modifier(LiveViewTap(enabled: preview != nil, action: openLive))
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                        Text(title).font(.callout.weight(.semibold)).lineLimit(1).truncationMode(.middle)
                            .renamable(address: address, current: title)
                        StatusPill(state: state)
                    }
                    Text(details.joined(separator: " · "))
                        .font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.tail)
                        .textSelection(.enabled)
                    if let expiry, state == "running" || state == "paused" {
                        TimelineView(.periodic(from: .now, by: 30)) { context in
                            Label(ThrowawayFormat.countdown(to: expiry, now: context.date), systemImage: "timer")
                                .font(.caption2).foregroundStyle(.tertiary)
                        }
                    }
                }
                Spacer(minLength: 6)
                HStack(spacing: 8) {
                    if preview != nil {
                        Button { openLive() } label: { Image(systemName: "play.rectangle") }
                            .buttonStyle(.borderless)
                            .help("Open live view")
                    }
                    actions()
                }
            }
            .contentShape(Rectangle())
            .onChange(of: preview?.id) { _, _ in
                if preview == nil { closePreview() }
            }
            .onDisappear { closePreview() }
            // Shown inline, not as a popover: a MenuBarExtra window does not reliably present popovers.
            if let previewModel, preview != nil {
                VMPreviewThumbnail(preview: previewModel)
                    .contentShape(Rectangle())
                    .onTapGesture { openLive() }
                    .transition(.opacity)
            }
            if InstanceProgress.shows(state) || progressStep?.hasPrefix("app-") == true {
                InstanceProgress(kind: kind, state: state, step: progressStep, percent: progressPercent,
                                 appTitle: appTitle, runnerName: runnerName, runnerOnline: runnerOnline)
            }
        }
        .padding(10)
        .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(.quaternary.opacity(0.35)))
        .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(.separator.opacity(0.5)))
        .onHover { hoverChanged($0) }
    }
}

/// A small coloured dot and word for an instance's state.
struct StatusPill: View {
    let state: String?
    var body: some View {
        HStack(spacing: 4) {
            Circle().fill(color).frame(width: 6, height: 6)
            Text(text).font(.caption2.weight(.medium)).foregroundStyle(.secondary)
        }
        .padding(.horizontal, 6).padding(.vertical, 2)
        .background(color.opacity(0.12), in: Capsule())
        .accessibilityElement(children: .combine)
    }
    private var text: String {
        switch state ?? "" {
        case "running": return "Running"
        case "provisioning": return "Starting"
        case "requested": return "Queued"
        case "stopping": return "Stopping"
        case "stopped": return "Stopped"
        case "paused": return "Paused"
        case "failed": return "Failed"
        default: return (state ?? "Unknown").capitalized
        }
    }
    private var color: Color {
        switch state ?? "" {
        case "running": return .green
        case "provisioning", "requested", "stopping": return .orange
        case "failed": return .red
        default: return .gray
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
    @State private var lifetime = 24
    @State private var reach = "network"
    @State private var runners: [SandboxRunner] = []
    @State private var runnerId = ""
    @State private var catalog: SandboxImageCatalog?
    @State private var imageId = ""
    @State private var loadError: String?
    @State private var loading = false

    private var runner: SandboxRunner? { runners.first { $0.id == runnerId } }
    private var containersOnly: Bool { runner?.containersOnly == true || catalog?.runner.containersOnly == true }
    private var images: [SandboxImage] {
        (catalog?.images ?? []).filter { $0.supports(kind) && (kind == "devcontainer" || $0.checksumPending != true) }
    }
    private var selectedImage: SandboxImage? { images.first { $0.id == imageId } }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 10) {
                Image(systemName: "square.stack.3d.up.fill").font(.title2).foregroundStyle(Color.accentColor)
                VStack(alignment: .leading, spacing: 1) {
                    Text("New Virtual Machine or Development Container").font(.headline)
                    Text("It joins your private network like your other devices.").font(.caption).foregroundStyle(.secondary)
                }
            }
            Form {
                if !runners.isEmpty {
                    Picker("Create on", selection: $runnerId) {
                        ForEach(runners) { r in
                            Label {
                                Text(runnerTitle(r))
                            } icon: {
                                Image(systemName: r.managed == true ? "externaldrive.connected.to.line.below" : r.thisComputer == true ? "laptopcomputer" : "desktopcomputer")
                            }
                            .tag(r.id)
                        }
                    }
                }
                Picker("Type", selection: $kind) {
                    Label("Development container", systemImage: "shippingbox").tag("devcontainer")
                    Label("Virtual machine", systemImage: "desktopcomputer").tag("vm")
                }
                .pickerStyle(.segmented)
                .disabled(containersOnly)
                Picker("Image", selection: $imageId) {
                    ForEach(images) { image in
                        Label {
                            Text(image.version.map { "\(image.name) \($0)" } ?? image.name)
                        } icon: {
                            InstanceIcon.menuImage(image.iconFamily)
                        }
                        .tag(image.id)
                    }
                }
                .disabled(images.isEmpty)
                Picker("Size", selection: $size) {
                    Text("Small · 2 CPU, 2 GB").tag("small")
                    Text("Medium · 4 CPU, 8 GB").tag("medium")
                    Text("Large · 8 CPU, 16 GB").tag("large")
                }
                if kind == "devcontainer" {
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
            HStack(alignment: .top, spacing: 10) {
                InstanceIcon(family: selectedImage?.iconFamily ?? (kind == "devcontainer" ? "devcontainer" : "linux"), size: 34)
                VStack(alignment: .leading, spacing: 2) {
                    Text(selectedImage.map { [$0.name, $0.version].compactMap { $0 }.joined(separator: " ") } ?? "Choose an image")
                        .font(.callout.weight(.semibold))
                    Text(explanation).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 10, style: .continuous).fill(.quaternary.opacity(0.35)))
            if let reason = runner?.lockedReason, runner?.locked == true {
                Text(reason).font(.caption).foregroundStyle(.orange)
            } else if catalog?.runner.hostingEnabled == false {
                Text("That Mac isn't set up to host instances. Turn on hosting in Settings › Virtual Machine Hosting.").font(.caption).foregroundStyle(.orange)
            }
            if let loadError { Text(loadError).font(.caption).foregroundStyle(.red) }
            if let message = hosting.message { Text(message).font(.caption).foregroundStyle(.red).textSelection(.enabled) }
            HStack {
                if loading || hosting.creating { ProgressView().controlSize(.small) }
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(hosting.creating ? "Creating…" : "Create") { Task { await create() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(hosting.creating || loading || imageId.isEmpty || catalog == nil || runner?.locked == true)
            }
        }
        .padding(20)
        .frame(width: 480)
        .task { await start() }
        .onChange(of: runnerId) { _, _ in Task { await loadCatalog() } }
        .onChange(of: kind) { _, _ in pickImage() }
    }

    private func runnerTitle(_ r: SandboxRunner) -> String {
        var title = r.name
        if r.thisComputer == true { title += " (this Mac)" }
        if r.managed == true { title += " · development containers only" }
        if r.locked == true { title += " · unavailable" }
        return title
    }

    private var explanation: String {
        let place = runner.map { $0.thisComputer == true ? "this Mac" : $0.name } ?? "this Mac"
        if kind == "devcontainer" {
            return "A development container on \(place). Temporary: deleted automatically after the time you choose."
        }
        if selectedImage?.appProfile == "home-assistant" {
            return "A Home Assistant virtual machine on \(place). Kept until you remove it; open it from its Connect menu once it is running."
        }
        if selectedImage?.appProfile == "jellyfin" {
            return "A Jellyfin media server on \(place), on your private network only. Your shared drive is its media library; open it from Connect, or in the Jellyfin app at the address shown."
        }
        return "A Linux virtual machine on \(place). Kept until you remove it."
    }

    private func start() async {
        hosting.clearMessage()
        loading = true
        runners = await hosting.loadRunners(model)
        // This Mac when it can host; otherwise the first computer that can. The owner can pick any.
        runnerId = runners.first(where: { $0.thisComputer == true && $0.locked != true })?.id
            ?? runners.first(where: { $0.locked != true })?.id
            ?? runners.first?.id ?? ""
        if runners.isEmpty { await loadCatalog() } // onChange does it otherwise
        loading = false
    }

    private func loadCatalog() async {
        loading = true; loadError = nil; catalog = nil
        defer { loading = false }
        do {
            let c = try await hosting.loadImages(model, runner: runnerId.isEmpty || runner?.thisComputer == true ? nil : runnerId)
            catalog = c
            if containersOnly { kind = "devcontainer" }
            pickImage()
            if c.images.isEmpty { loadError = "No compatible images for that computer yet." }
        } catch {
            loadError = "Could not load images: \(error.localizedDescription)"
        }
    }

    private func pickImage() {
        if !images.contains(where: { $0.id == imageId }) { imageId = images.first?.id ?? "" }
    }

    private func create() async {
        guard let catalog else { return }
        hosting.clearMessage()
        // Development containers are temporary, virtual machines persistent.
        let lifecycle = kind == "devcontainer" ? "ephemeral" : "persistent"
        let request = SandboxCreateRequest(imageId: imageId, runnerHostId: runnerId.isEmpty ? catalog.runner.id : runnerId, size: size, kind: kind,
                                           lifecycle: lifecycle, lifetimeHours: kind == "devcontainer" ? lifetime : nil, reach: reach)
        if await hosting.create(request, model: model) { dismiss() }
    }
}
