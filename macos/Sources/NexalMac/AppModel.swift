import AppKit
import Foundation
import SwiftUI

@MainActor
final class AppModel: ObservableObject {
    /// The deployed development coordinator, prefilled so a fresh install has a
    /// working destination without the owner typing one. The connector is a
    /// client of a hosted service; it does not discover peers on the LAN and
    /// nothing here probes the local network. `config.go` independently rejects
    /// any origin that is not https, so this default is the only prefilled value
    /// that path will accept.
    static let defaultCoordinator = "https://coordinator-dev.nexal.systems"
    @Published var coordinator = AppModel.defaultCoordinator
    @Published var hostName = Host.current().localizedName ?? "My Mac"
    @Published var enrollmentCode = ""
    @Published var memoryMiB = 256
    @Published var reserveMiB = 4096
    @Published private(set) var selection: ExecutableSelection?
    @Published private(set) var status: ConnectorStatus?
    @Published private(set) var busy = false
    @Published private(set) var message: String?
    @Published private(set) var processOwned = false
    @Published private(set) var lastUpdated: Date?
    @Published private(set) var enrollmentPresentation = EnrollmentPresentation()
    /// Bounded, in-memory only, appended by the single status poll (§26.4).
    @Published private(set) var history = ConnectorHistory()
    /// Transport and RDMA facts as reported through the one seam (§26.2/§26.3).
    /// Starts as "nothing is reporting", which is the honest state at launch.
    @Published private(set) var capability: TransportCapability
    @Published var consent = false
    @Published var localPreview = false {
        didSet {
            if oldValue != localPreview {
                enrollmentCode = ""
                consent = false
            }
        }
    }
    private var child: Process?
    /// The only place a capability source is named. Swapping the implementation
    /// changes no view, presenter or chart.
    private let capabilitySource: TransportCapabilityProviding

    var selectedConfig: URL {
        localPreview ? ConnectorProcess.localPreviewConfigURL : ConnectorProcess.configURL
    }
    var configurationExists: Bool {
        FileManager.default.fileExists(atPath: selectedConfig.path)
    }
    var title: String {
        guard let status else { return "Not connected" }
        return status.paused ? "Paused" : "Private resources enabled"
    }
    var contributes: Bool { status.map { !$0.paused } ?? false }
    var transport: TransportPresentation { TransportPresentation(capability: capability) }
    var rdma: RDMAPresentation { RDMAPresentation(capability: capability) }
    var resourceSharing: ResourceSharingPresentation {
        ResourceSharingPresentation(status: status)
    }
    var manualAcceptance: ManualAcceptancePresentation {
        ManualAcceptancePresentation(status: status)
    }
    var manualAcceptanceUnavailableReason: String? {
        ManualAcceptancePresentation.unavailableReason(
            localPreview: localPreview, hasExecutable: selection != nil,
            configurationExists: configurationExists, status: status)
    }
    var showsEnrollmentConfirmation: Bool {
        enrollmentPresentation.showsConfirmation(for: selectedConfig)
    }

    func useAnotherEnrollmentCode() {
        guard !busy else { return }
        enrollmentCode = ""
        consent = false
        enrollmentPresentation.beginReplacement(for: selectedConfig)
    }

    init(capabilitySource: TransportCapabilityProviding = ConnectorStatusCapabilitySource()) {
        self.capabilitySource = capabilitySource
        capability = capabilitySource.capability(from: nil)
        // Stored only after explicit selection. Credentials/config stay in Go.
        let defaults = UserDefaults.standard
        if let path = defaults.string(forKey: "approvedConnectorPath"),
           let hash = defaults.string(forKey: "approvedConnectorHash"),
           let approved = try? ExecutableSelection.approve(URL(fileURLWithPath: path)),
           approved.sha256 == hash {
            selection = approved
        }
    }

    func chooseConnector() {
        let panel = NSOpenPanel()
        panel.title = "Choose your installed Nexal Go connector"
        panel.message = "Select nexal in this app’s Contents/Helpers or ~/Library/Application Support/Nexal/bin. Review its code signature first."
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        panel.directoryURL = ExecutableSelection.supportDirectory.appendingPathComponent("bin")
        guard panel.runModal() == .OK, let url = panel.url else { return }
        approve(url)
    }

    func chooseBundledConnector() { approve(ExecutableSelection.bundledExecutable) }

    private func approve(_ url: URL) {
        do {
            let approved = try ExecutableSelection.approve(url)
            selection = approved
            UserDefaults.standard.set(approved.url.path, forKey: "approvedConnectorPath")
            UserDefaults.standard.set(approved.sha256, forKey: "approvedConnectorHash")
            message = "Connector selected. No service was started."
        } catch { message = error.localizedDescription }
    }

    private func invoke(_ command: CLICommand, input: Data? = nil) async throws -> Data {
        guard let selection else { throw ShellError.noExecutable }
        let config = selectedConfig
        return try await Task.detached(priority: .userInitiated) {
            try ConnectorProcess.execute(selection, command, stdin: input, config: config)
        }.value
    }

    func initializeAndEnroll() async {
        guard !busy, consent, !showsEnrollmentConfirmation else { return }
        busy = true
        defer { busy = false; enrollmentCode = "" }
        do {
            let code = enrollmentCode.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !code.isEmpty, code.utf8.count <= 255,
                  !code.contains("\n"), !code.contains("\r") else { throw ShellError.invalidCode }
            if !configurationExists {
                if localPreview {
                    _ = try await invoke(.initializeLocalPreview(name: hostName,
                                               memoryMiB: memoryMiB, reserveMiB: reserveMiB))
                } else {
                    _ = try await invoke(.initialize(coordinator: coordinator, name: hostName,
                                               memoryMiB: memoryMiB, reserveMiB: reserveMiB))
                }
            }
            _ = try await invoke(.enroll, input: Data((code + "\n").utf8))
            enrollmentPresentation.recordSuccess(for: selectedConfig)
            message = "Enrolled with contribution paused. Start the connector, then explicitly enable private resources."
        } catch { message = error.localizedDescription }
    }

    func start() async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        do {
            // An app-owned daemon may still be starting after a failed first
            // status read. Retry that daemon; never launch a second process.
            if child != nil {
                try await updateStatus()
                return
            }
            // Attach to an existing Go service rather than launch a duplicate.
            if let data = try? await invoke(.status),
               let existing = try? ConnectorStatus.decode(data) {
                status = existing
                enrollmentPresentation.observeHost(existing.hostId, for: selectedConfig)
                lastUpdated = Date()
                observe(existing)
                message = "Connected to an existing connector. Quitting this app will not stop it."
                return
            }
            guard let selection else { throw ShellError.noExecutable }
            let process = try ConnectorProcess.make(selection, .run, config: selectedConfig)
            process.standardInput = FileHandle.nullDevice
            process.standardOutput = FileHandle.nullDevice
            process.standardError = FileHandle.nullDevice
            process.terminationHandler = { [weak self] completed in
                Task { @MainActor in
                    guard let self, self.child === completed else { return }
                    self.child = nil
                    self.processOwned = false
                    self.status = nil
                    self.capability = self.capabilitySource.capability(from: nil)
                    self.message = "Connector stopped. Review the Go CLI configuration before restarting."
                }
            }
            try process.run()
            child = process
            processOwned = true
            message = "Connector started. Waiting for its local status…"
            try await Task.sleep(for: .milliseconds(750))
            try await updateStatus()
        } catch { message = error.localizedDescription }
    }

    private func updateStatus() async throws {
        status = try ConnectorStatus.decode(try await invoke(.status))
        enrollmentPresentation.observeHost(status?.hostId, for: selectedConfig)
        lastUpdated = Date()
        observe(status)
    }

    /// The single fan-out point for every view: one poll updates the reported
    /// capability and appends one history sample. No chart owns a timer.
    private func observe(_ status: ConnectorStatus?) {
        capability = capabilitySource.capability(from: status)
        history.record(status)
    }

    func refresh() async {
        guard !busy, selection != nil else { return }
        busy = true
        defer { busy = false }
        do { try await updateStatus() }
        catch {
            status = nil
            lastUpdated = nil
            // Record the gap rather than dropping the poll, so the charts stop
            // extending instead of implying continued measurement.
            observe(nil)
            message = error.localizedDescription
        }
    }

    func acceptJobsNow() async {
        guard !busy, localPreview else { return }
        if let reason = manualAcceptanceUnavailableReason {
            message = reason
            return
        }
        if status == nil { await start() }
        guard status != nil else { return } // Preserve the actual startup failure.
        if let reason = manualAcceptanceUnavailableReason {
            message = reason
            return
        }
        busy = true
        defer { busy = false }
        do {
            _ = try await invoke(.acceptJobs)
            try await updateStatus()
            guard manualAcceptance.isActive else {
                message = "The connector has not confirmed an active acceptance window. Refresh and check its status; no permission is assumed."
                return
            }
            message = "The connector confirmed private zero-cost CPU permission until the displayed time. Memory, lease and connection checks still apply. Pause cancels this permission and active work."
        } catch {
            status = nil
            lastUpdated = nil
            message = "Could not confirm private-job permission. \(error.localizedDescription) Refresh before retrying; the connector may already have received the request."
        }
    }

    func setContribution(_ enabled: Bool) async {
        guard !busy, status != nil else { return }
        busy = true
        defer { busy = false }
        do {
            _ = try await invoke(enabled ? .resume : .pause)
            try await updateStatus()
            message = enabled
                ? "Private resource policy enabled. Execution still requires Go admission and release gates."
                : "Paused by the owner; the Go connector cancels active work."
        } catch {
            status = nil // Never claim a pause/resume succeeded when confirmation failed.
            message = error.localizedDescription
        }
    }

    func quit() {
        if let child, child.isRunning { child.terminate() }
        NSApplication.shared.terminate(nil)
    }
}
