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
    /// What the connector is doing RIGHT NOW, in the owner's language.
    ///
    /// `busy` alone only says "something is happening", so every operation looked
    /// identical: a spinner with no subject. Enrolling contacts a coordinator,
    /// minting a pairing contacts it again, starting the agent does not contact it
    /// at all -- and when one of those stalled there was nothing on screen saying
    /// which. This names the phase so a stall is attributable.
    ///
    /// Set only through `perform`, so it cannot be left stale after a throw: the
    /// same defer that clears `busy` clears this.
    @Published private(set) var activity: String?
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
    /// The pairing being shown, if any. Held in memory only: the module matrix
    /// encodes the claim token, so it is never persisted, never logged and dropped
    /// as soon as the pairing is finished with.
    @Published private(set) var pairing: PairingPresentation?
    /// The connector's own explanation when a pairing could not be minted — the
    /// feature being switched off on the coordinator, or a host credential the
    /// coordinator no longer accepts. Kept separate from `message` so the reason
    /// stays next to the pairing controls rather than scrolling away in the footer.
    @Published private(set) var pairingProblem: String?
    /// The role the owner picked. Defaults to receiver, which is the direction a
    /// Mac is used in first; nothing is minted until the owner asks.
    @Published var pairingRole: PairingRole = .receiver
    /// Ticks once a second while a pairing is live, purely so the countdown text
    /// re-renders. It does not poll, and it never decides that a pairing expired.
    @Published private(set) var pairingTick = Date()
    @Published var developmentEnvironment = false {
        didSet {
            if oldValue != developmentEnvironment {
                enrollmentCode = ""
                consent = false
            }
        }
    }
    private var child: Process?
    /// The pairing poll and the countdown timer, cancelled together. Separate from
    /// the panel's single status poll because a pairing lives for five minutes and
    /// must be watched more closely than that poll's ten-second cadence.
    private var pairingWatch: Task<Void, Never>?
    /// The only place a capability source is named. Swapping the implementation
    /// changes no view, presenter or chart.
    private let capabilitySource: TransportCapabilityProviding

    var selectedConfig: URL {
        developmentEnvironment ? ConnectorProcess.developmentConfigURL : ConnectorProcess.configURL
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
            developmentEnvironment: developmentEnvironment, hasExecutable: selection != nil,
            configurationExists: configurationExists, status: status)
    }
    var pairingUnavailableReason: String? {
        PairingPresentation.unavailableReason(
            hasExecutable: selection != nil, configurationExists: configurationExists,
            status: status)
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
        } else if let bundled = try? ExecutableSelection.approve(ExecutableSelection.bundledExecutable) {
            // Adopt the connector shipped INSIDE this app bundle without asking.
            //
            // Choosing it was never a real decision. The helper lives in
            // Contents/Helpers of this same signed, notarized bundle; it is
            // codesigned as part of the app, and Gatekeeper already validated it
            // before the app could launch. Making the owner pick it turned a
            // certainty into a chore, and left every other control inert until
            // they had performed a step whose only correct answer was "the one
            // that shipped with me".
            //
            // This is not a weakening. ExecutableSelection.approve still runs in
            // full: the path must be one of the two allowed locations, must not be
            // a symlink, must be a regular file not writable by group or other,
            // must be owned by this user or root, must be within the size bounds,
            // and is pinned by SHA-256 and revalidated before every single
            // invocation. An adopted binary that is later swapped still fails
            // revalidate() with executableChanged, exactly as a hand-picked one
            // would. "Choose installed..." remains for the override case.
            selection = bundled
            defaults.set(bundled.url.path, forKey: "approvedConnectorPath")
            defaults.set(bundled.sha256, forKey: "approvedConnectorHash")
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
        activity = "Contacting the coordinator\u{2026}"
        busy = true
        defer { busy = false; activity = nil; enrollmentCode = "" }
        do {
            let code = enrollmentCode.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !code.isEmpty, code.utf8.count <= 255,
                  !code.contains("\n"), !code.contains("\r") else { throw ShellError.invalidCode }
            if !configurationExists {
                activity = "Creating this Mac\u{2019}s configuration\u{2026}"
                if developmentEnvironment {
                    _ = try await invoke(.initializeDevelopment(name: hostName,
                                               memoryMiB: memoryMiB, reserveMiB: reserveMiB))
                } else {
                    _ = try await invoke(.initialize(coordinator: coordinator, name: hostName,
                                               memoryMiB: memoryMiB, reserveMiB: reserveMiB))
                }
            }
            activity = "Authenticating with the coordinator\u{2026}"
            _ = try await invoke(.enroll, input: Data((code + "\n").utf8))
            enrollmentPresentation.recordSuccess(for: selectedConfig)
            message = "Enrolled with contribution paused. Start the connector, then explicitly enable private resources."
        } catch { message = error.localizedDescription }
    }

    func start() async {
        guard !busy else { return }
        activity = "Starting the connector\u{2026}"
        busy = true
        defer { busy = false; activity = nil }
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
        activity = "Checking connector status\u{2026}"
        busy = true
        defer { busy = false; activity = nil }
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
        guard !busy, developmentEnvironment else { return }
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
        activity = "Updating resource policy\u{2026}"
        busy = true
        defer { busy = false; activity = nil }
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

    // MARK: - Phone pairing
    //
    // Every step goes through the ONE seam: `invoke` runs the Go CLI with the
    // bounded execution every other command uses. This app does not mint, poll,
    // cancel, encode or validate a pairing itself — it displays what the connector
    // reports. That is why there is no QR encoder and no coordinator client here.

    /// Mint a pairing and start watching it.
    func startPairing() async {
        guard !busy else { return }
        if let reason = pairingUnavailableReason {
            pairingProblem = reason
            return
        }
        activity = "Requesting a pairing code\u{2026}"
        busy = true
        defer { busy = false; activity = nil }
        pairingProblem = nil
        // A second pairing must not leave the first one open on the coordinator,
        // where it would stay scannable until it expired.
        await cancelPairing(silently: true)
        do {
            let minted = try PairingMint.decode(try await invoke(.pair(role: pairingRole)))
            guard let presentation = PairingPresentation(mint: minted) else {
                throw ShellError.invalidPairing
            }
            pairing = presentation
            watchPairing()
            message = "Pairing code ready. Scan it in nexal@home; it expires shortly and nothing is shared by scanning alone."
        } catch {
            pairing = nil
            // The connector's text is shown verbatim because it names the actual
            // cause — the feature being off on the coordinator, an unaccepted host
            // credential, an unreachable coordinator — which this app cannot infer.
            pairingProblem = error.localizedDescription
        }
    }

    /// Cancel the displayed pairing. `silently` is used when replacing one pairing
    /// with another, where a failure to cancel the old one must not overwrite the
    /// message about the new one.
    func cancelPairing(silently: Bool = false) async {
        guard let current = pairing else { return }
        pairingWatch?.cancel()
        pairingWatch = nil
        do {
            _ = try await invoke(.cancelPairing(pairingId: current.pairingId))
            pairing = nil
            if !silently { message = "Pairing cancelled. The code can no longer be scanned." }
        } catch {
            // The pairing is NOT cleared here: if the cancellation could not be
            // confirmed, the code may still be live on the coordinator, and hiding
            // it would tell the owner it was dead when it is not.
            if !silently {
                pairingProblem = "Could not confirm the cancellation. \(error.localizedDescription) The code may still be scannable until it expires."
                watchPairing()
            }
        }
    }

    /// Poll this pairing's status through the CLI until it stops being live.
    private func watchPairing() {
        pairingWatch?.cancel()
        pairingWatch = Task { [weak self] in
            while !Task.isCancelled {
                do { try await Task.sleep(for: .seconds(1)) } catch { return }
                guard let self else { return }
                let keepGoing = await self.refreshPairing()
                if !keepGoing { return }
            }
        }
    }

    /// One tick of the pairing watch. Returns false when there is nothing left to
    /// watch. The status is polled every third tick: the countdown needs a redraw
    /// every second, but the coordinator does not need a request every second.
    private func refreshPairing() async -> Bool {
        guard let current = pairing else { return false }
        pairingTick = Date()
        guard current.status.isLive else { return false }
        guard Int(pairingTick.timeIntervalSince1970) % 3 == 0 else { return true }
        do {
            let report = try PairingStatusReport.decode(
                try await invoke(.pairingStatus(pairingId: current.pairingId)))
            let updated = current.updated(with: report.pairingStatus)
            pairing = updated
            if updated.status == .scanned {
                message = "Your phone claimed this pairing. Approve what it may use on the phone; nothing is shared by pairing alone."
            }
            return updated.status.isLive
        } catch {
            // A failed poll says nothing about the pairing, so the code stays on
            // screen and the reason is shown. Stopping here would silently strand
            // a pairing that is still live.
            pairingProblem = "Could not read the pairing status. \(error.localizedDescription)"
            return true
        }
    }

    func quit() {
        pairingWatch?.cancel()
        pairingWatch = nil
        if let child, child.isRunning { child.terminate() }
        NSApplication.shared.terminate(nil)
    }
}
