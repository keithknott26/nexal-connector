import AppKit
import Foundation
import SwiftUI

@MainActor
final class AppModel: ObservableObject {
    private let timeMachineDiscovery = TimeMachineDiscovery()
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
    /// Last Wake-on-LAN outcome per mesh peer id, shown under its quick links.
    @Published private(set) var wakeStatus: [String: String] = [:]
    /// Public IP and location per mesh peer id, filled in after each status poll.
    @Published private(set) var peerNetInfo: [String: PeerNetInfo] = [:]
    /// This Mac's own public IP and location, shown the same way as a peer's.
    @Published private(set) var ownNetInfo = PeerNetInfo()
    /// The exit route this Mac sends all internet traffic through, or nil.
    /// Remembered across launches and re-applied when the service restarts.
    @Published private(set) var exitRoute: String?
    private var desiredExitRoute = UserDefaults.standard.string(forKey: AppModel.exitRouteKey)
    @Published private(set) var peerExitRoutes: [String: String] = UserDefaults.standard.dictionary(forKey: "peerExitRouteIDs") as? [String: String] ?? [:]
    private var peerExitDeviceIDs: [String: String] = UserDefaults.standard.dictionary(forKey: "peerExitDeviceIDs") as? [String: String] ?? [:]
    @Published private(set) var exitRouteStatus: [String: String] = [:]
    private var pendingExitTeardown: [String: String] = UserDefaults.standard.dictionary(forKey: "pendingExitTeardown") as? [String: String] ?? [:]
    /// Exit routes the coordinator has offered this Mac.
    @Published private(set) var availableExitRoutes: Set<String> = []
    @Published private(set) var exitRouteBusy = false
    private var exitRouteCheckedAt: Date?
    static let exitRouteKey = "exitRouteID"
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
    /// Post-quantum tunnel evidence, read from tunnel-evidence.json beside the config.
    /// Defaults to an empty value so an agent that has never run reads as "not
    /// configured" rather than leaving the indicator blank.
    @Published private(set) var tunnelEvidence = TunnelEvidence()
    /// The other Macs, from `nexal peers-view`. Reachability is classified by the
    /// connector; the app does not decide which address is dialable.
    @Published private(set) var peersView = PeersView()
	@Published private(set) var timeMachine: TimeMachineReport?
    /// The "Leave neXal network" flow, rendered as a banner at the top of the panel.
    /// nil when no leave has been asked for.
    @Published private(set) var leavePhase: LeavePhase?
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
	private var lastTimeMachineCheck: Date?
    /// The only place a capability source is named. Swapping the implementation
    /// changes no view, presenter or chart.
    private let capabilitySource: TransportCapabilityProviding

    var selectedConfig: URL {
        developmentEnvironment ? ConnectorProcess.developmentConfigURL : ConnectorProcess.configURL
    }
    var configurationExists: Bool {
        FileManager.default.fileExists(atPath: selectedConfig.path)
    }
    /// The coordinator origin saved in the selected config.json, if any. `init`
    /// writes it once and every later command reads it, so a config left by an
    /// older build keeps pointing at its old origin even after an app update.
    var configuredCoordinator: String? {
        guard let data = try? Data(contentsOf: selectedConfig),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { return nil }
        return object["coordinator"] as? String
    }
    /// Non-nil when the saved config targets an origin this build does not know.
    var coordinatorMismatch: String? {
        guard let saved = configuredCoordinator else { return nil }
        let known = [CoordinatorOrigins.development, CoordinatorOrigins.production]
        return known.contains(saved) ? nil : saved
    }
    var hasPersistedHostIdentity: Bool {
        ConnectorProcess.hasPersistedHostIdentity(at: selectedConfig)
    }
    var hasUnfinishedEnrollment: Bool { ConnectorProcess.hasUnfinishedEnrollment(at: selectedConfig) }
    var title: String {
        guard let status else { return "Not connected" }
        return status.paused ? "Paused" : "Private resources enabled"
    }
    var contributes: Bool { status.map { !$0.paused } ?? false }

    /// Which single thing the window should show. The decision itself lives in
    /// `ConnectorStage` so it can be unit-tested; this only supplies the four facts
    /// it reads.
    ///
    /// `lastUpdated` is the honest test for "has a poll come back", rather than
    /// `status != nil`: status is also nil after a failed poll, and treating launch
    /// and failure as the same state would show "not running" before anything had
    /// been asked.
    var stage: ConnectorStage {
        ConnectorStage.derive(hasAnswered: lastUpdated != nil,
                              isRunning: status != nil,
                              isEnrolled: status?.hostId?.isEmpty == false,
                              pairing: pairing,
                              now: pairingTick)
    }
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
        // Set up the secure networking service as soon as the app starts, so
        // nobody has to run a terminal command after installing the DMG. Only
        // the installed app does this: tests and `swift run` have no bundle.
        if Bundle.main.bundleURL.pathExtension == "app" {
            Task { [weak self] in
                await self?.ensureNetworkServiceAtLaunch()
                await self?.keepAgentRunning()
            }
        }
    }

    /// The panel's own poll only runs while the menu-bar panel is open, so on
    /// its own the agent would not start after launch or an update until the
    /// owner clicked the icon. This background loop starts (or re-attaches to)
    /// the agent at launch and restarts it within 30 s if it stops.
    private func keepAgentRunning() async {
        while !Task.isCancelled {
            if hasPersistedHostIdentity, child == nil || status == nil {
                await refresh()
            }
            do { try await Task.sleep(for: .seconds(30)) } catch { return }
        }
    }

    /// One administrator prompt at launch when the service is missing or stopped
    /// (first install, or after an update left it stopped). Cancelling is
    /// respected: the panel keeps the Install button and says why it matters.
    func ensureNetworkServiceAtLaunch() async {
        guard (try? NetworkService.validatedHelper()) != nil else { return }
        // installNetworkService() rejoins on success; otherwise rejoin directly.
        if NetworkService.isRunning && NetworkService.serviceUsesOtherBinary {
            // A service registered from an older copy of the app (often now in
            // the Trash) keeps running that binary, and the firewall blocks it.
            AgentLog.note("network service runs \(NetworkService.registeredServiceBinary ?? "?"); reinstalling from this app")
            await installNetworkService()
        } else if NetworkService.isRunning {
            // Already-installed Macs never re-run install(), so fix the firewall
            // here if "Block all incoming connections" or stealth mode is on.
            await repairFirewallIfNeeded()
            await rejoinNetworkIfEnrolled()
        } else {
            await installNetworkService()
        }
    }

    /// One administrator prompt when the macOS firewall would block peers or
    /// the service has lazy connections on.
    func repairFirewallIfNeeded() async {
        let (firewall, lazy, wake) = await Task.detached(priority: .utility) {
            (NetworkService.firewallBlocksPeers, NetworkService.lazyConnectionsOn, NetworkService.wakeForNetworkOff)
        }.value
        guard firewall || lazy || wake else { return }
        do {
            try await Task.detached(priority: .userInitiated) {
                try NetworkService.repairHostSettings(firewall: firewall, lazy: lazy, wake: wake)
            }.value
            message = "Network settings updated: connections from your other computers are allowed, connections stay on, and Wake for network access is on."
        } catch {
            pairingProblem = error.localizedDescription
        }
    }

    /// An enrolled Mac only joined its network while a pairing was being
    /// polled, so after a reinstall or reboot it could sit off the network
    /// forever. Rejoin with the saved credential whenever the service is up.
    func rejoinNetworkIfEnrolled() async {
        guard NetworkService.isRunning, selection != nil, configurationExists,
              hasPersistedHostIdentity || hasUnfinishedEnrollment else { return }
        do { _ = try await invoke(.rejoinNetwork) }
        catch { pairingProblem = error.localizedDescription }
    }

    func chooseConnector() {
        let panel = NSOpenPanel()
        panel.title = "Choose the neXal connector"
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


    func canary(action: String) async throws -> CanaryReply {
        try JSONDecoder().decode(CanaryReply.self, from: try await invoke(.canary(action: action)))
    }

    func honeypot(action: String) async throws -> HoneypotReply {
        try JSONDecoder().decode(HoneypotReply.self, from: try await invoke(.honeypot(action: action)))
    }

    /// Throwaway hosts on the network and Connect, through the connector (this app has no coordinator client).
    /// `action` is "list" or "connect"; a public key, when needed, goes in on stdin.
    func sandbox(action: String, id: String?, kind: String?, input: Data? = nil) async throws -> Data {
        try await invoke(.sandbox(action: action, id: id, kind: kind), input: input)
    }

    @Published var guestInvitationCode = ""
    @Published private(set) var guestInvitationProblem: String?
    var guestAccess: GuestAccessRecord? { GuestAccessRecord.read(at: selectedConfig) }

    func redeemGuestInvitation() async {
        guard !busy else { return }
        busy = true; activity = "Checking your invitation…"; guestInvitationProblem = nil
        defer { busy = false; activity = nil }
        let code = guestInvitationCode.trimmingCharacters(in: .whitespacesAndNewlines)
        guard code.utf8.count <= 32, !code.contains("\n"), !code.contains("\r") else {
            guestInvitationProblem = "Enter the eight-character invitation code."; return
        }
        do {
            if !configurationExists {
                if developmentEnvironment {
                    _ = try await invoke(.initializeDevelopment(name: hostName, memoryMiB: memoryMiB, reserveMiB: reserveMiB))
                } else {
                    _ = try await invoke(.initialize(coordinator: coordinator, name: hostName, memoryMiB: memoryMiB, reserveMiB: reserveMiB))
                }
            }
            if !NetworkService.isRunning {
                activity = "Installing secure networking…"
                try await Task.detached(priority: .userInitiated) { try NetworkService.install() }.value
            }
            activity = "Redeeming your invitation…"
            let reply = try JSONDecoder().decode(GuestAccessResponse.self,
                from: try await invoke(.redeemGuestInvitation, input: Data((code + "\n").utf8)))
            guard reply.status != "provisioning" else {
                guestInvitationProblem = reply.message ?? "Invitation accepted. Retry the same code shortly to finish joining."; return
            }
            guard let grant = reply.guestAccess, !grant.isExpired() else {
                guestInvitationProblem = "This invitation has expired. Ask the inviter for a new code."; return
            }
            let configuration = selectedConfig
            activity = "Installing the automatic access-expiry guard…"
            try await Task.detached(priority: .userInitiated) {
                try NetworkService.installGuestExpiryGuard(config: configuration)
            }.value
            activity = "Joining the temporary network…"
            _ = try await invoke(.activateGuestInvitation)
            guestInvitationCode = ""
            message = "Temporary access is active until \(grant.deadline?.formatted(date: .abbreviated, time: .shortened) ?? grant.accessExpiresAt). Ask \(grant.inviterEmail) for a new code when it expires."
            busy = false
            await start()
        } catch { guestInvitationProblem = error.localizedDescription }
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
        activity = "Contacting neXal\u{2026}"
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
            activity = "Signing in to neXal\u{2026}"
            _ = try await invoke(.enroll, input: Data((code + "\n").utf8))
            enrollmentPresentation.recordSuccess(for: selectedConfig)
            message = "This Mac joined with resource sharing paused. Start neXal, then turn on resource sharing when you are ready."
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
            // An agent left running by an earlier install still runs the old
            // code; stop it so the current build is launched below.
            if let selection {
                let url = selection.url
                let stale = await Task.detached(priority: .userInitiated) { () -> [pid_t] in
                    let pids = StaleAgent.pids(for: url)
                    StaleAgent.stop(for: url)
                    return pids
                }.value
                if !stale.isEmpty { AgentLog.note("stopped stale agent(s) \(stale)") }
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
            // The agent logs sanitized JSON to stderr; keep it so a connector
            // that stops can be diagnosed (~/Library/Logs/Nexal/agent.log).
            process.standardError = AgentLog.handle() ?? FileHandle.nullDevice
            process.terminationHandler = { [weak self] completed in
                let code = completed.terminationStatus
                let reason = completed.terminationReason == .uncaughtSignal ? "signal \(code)" : "exit \(code)"
                AgentLog.note("agent stopped (\(reason))")
                Task { @MainActor in
                    guard let self, self.child === completed else { return }
                    self.child = nil
                    self.processOwned = false
                    self.status = nil
                    self.capability = self.capabilitySource.capability(from: nil)
                    self.message = "Connector stopped (\(reason)). Details: ~/Library/Logs/Nexal/agent.log"
                }
            }
            AgentLog.note("starting agent: \(selection.url.path)")
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
        // Paired again after leaving: the "Left the network" notice is stale.
        if leavePhase == .left, isLinked { leavePhase = nil }
        enrollmentPresentation.observeHost(status?.hostId, for: selectedConfig)
        lastUpdated = Date()
        observe(status)
    }

    /// The single fan-out point for every view: one poll updates the reported
    /// capability and appends one history sample. No chart owns a timer.
    private func observe(_ status: ConnectorStatus?) {
        capability = capabilitySource.capability(from: status)
        history.record(status)
        // A peer that has reconnected no longer needs its "wake sent" note.
        for peer in status?.mesh?.peers ?? [] where peer.lifecycle == "connected" {
            wakeStatus[peer.id] = nil
        }
    }

    @Published private(set) var refreshingPeerID: String?
    @Published private(set) var peerRefreshErrors: [String: String] = [:]
    @Published private(set) var peerRefreshedAt: [String: Date] = [:]

    /// Read live runtime evidence through the connector's existing status seam.
    /// A refresh never fabricates protection or restarts unrelated peer links.
    func refreshPeer(_ peer: ConnectorStatus.MeshPeer) async {
        guard !busy, selection != nil else { return }
        busy = true
        refreshingPeerID = peer.id
        peerRefreshErrors[peer.id] = nil
        activity = "Refreshing computer status…"
        defer { busy = false; refreshingPeerID = nil; activity = nil }
        do {
            try await updateStatus()
            await updateTunnelEvidence()
            await updatePeers()
            guard status?.mesh?.peers.contains(where: { $0.id == peer.id }) == true else {
                peerRefreshErrors[peer.id] = "This computer is no longer on your neXal network."
                return
            }
            peerRefreshedAt[peer.id] = Date()
        } catch {
            peerRefreshErrors[peer.id] = "Refresh failed: \(error.localizedDescription)"
        }
    }

    func refresh() async {
        guard !busy, selection != nil else { return }

        // An unenrolled Mac intentionally has no running agent: pair-v2 talks
        // directly to the coordinator and persists the host credential only after
        // the phone approves it. Polling the local API in this state produced the
        // alarming but expected "connector is not running" message every five
        // seconds, overwriting the live pairing instructions.
        guard hasPersistedHostIdentity else {
            status = nil
            lastUpdated = nil
            observe(nil)
            await updateTunnelEvidence()
            return
        }

        // Pairing has now durably authorized this Mac. Start (or attach to) the
        // connector automatically so the local API and tunnel come up without a
        // hidden second setup step. `start` first probes for an existing service
        // and never launches a duplicate.
        if status == nil {
            await start()
            await updateTunnelEvidence()
            await updatePeers()
            await updatePeerLocations()
			await updateTimeMachine()
            return
        }

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
        // Both feed the panel and neither may fail the poll: a missing evidence file is
        // the normal state before the agent has ever run, and peers-view is unavailable
        // while the agent is down. A throw here would blank the status the poll just
        // successfully read.
        await updateTunnelEvidence()
        await updatePeers()
        await updatePeerLocations()
		await updateTimeMachine()
        await updateExitRoutes()
    }

    /// First click on "Leave neXal network": ask inline. A dialog is not used
    /// because MenuBarExtra windows do not reliably present one.
    func requestLeave() {
        guard leavePhase?.inProgress != true else { return }
        guard isLinked else {
            leavePhase = .failed(reason: "This Mac is not linked to a neXal network right now, so there is nothing to leave.")
            return
        }
        leavePhase = .confirming
    }

    func cancelLeave() {
        if leavePhase == .confirming { leavePhase = nil }
    }

    func dismissLeaveNotice() {
        if leavePhase?.inProgress != true { leavePhase = nil }
    }

    func leaveNetwork() async {
        guard leavePhase == .confirming else { return }
        leavePhase = .preparing
        // The panel polls status every five seconds and that poll holds `busy`.
        // Returning here (as before) silently swallowed the click; wait it out.
        for _ in 0..<100 where busy {
            do { try await Task.sleep(for: .milliseconds(100)) } catch { break }
        }
        guard !busy else {
            leavePhase = .failed(reason: "Another operation is still running. Try again in a moment; this Mac is still on the network.")
            return
        }
        guard isLinked else {
            leavePhase = .failed(reason: "This Mac is not linked to a neXal network right now, so there is nothing to leave.")
            return
        }
        busy = true
        activity = "Preparing to leave the neXal network\u{2026}"
        pairingWatch?.cancel(); pairingWatch = nil
        pairing = nil; pairingProblem = nil; message = nil

        // The running agent holds the configuration lock for its whole life, and
        // leaving must rewrite that configuration. Stop the agent this app launched
        // first; `pair-v2 --leave` stops one started elsewhere by itself. `child` is
        // cleared before terminating so its handler does not report a crash.
        if let owned = child {
            child = nil
            processOwned = false
            if owned.isRunning {
                owned.terminate()
                await Task.detached(priority: .userInitiated) { owned.waitUntilExit() }.value
            }
        }

        leavePhase = .leaving
        activity = "Leaving the neXal network\u{2026}"
        do {
            _ = try await invoke(.leaveNetwork)
        } catch {
            busy = false; activity = nil
            leavePhase = .failed(reason: "\(error.localizedDescription) This Mac may still be on the network; try again.")
            return
        }
        status = nil; peersView = PeersView(); tunnelEvidence = TunnelEvidence(); timeMachine = nil
        enrollmentPresentation = EnrollmentPresentation()
        lastUpdated = nil
        observe(nil)
        busy = false; activity = nil
        leavePhase = .left

        // Back to pairing: show a fresh QR and manual code straight away. If a code
        // cannot be minted, the pairing screen shows why and offers the button.
        await startPairing()
    }

    /// Reads tunnel-evidence.json, which `nexal run` writes beside config.json.
    ///
    /// Read from disk rather than requested from the agent because the agent writes it
    /// on every observation and the file survives the agent being down -- which is
    /// precisely when the owner wants to know what the tunnel was last doing.
    private func updateTunnelEvidence() async {
        // Beside the SELECTED config, not always the production one: a development
        // profile lives in its own directory and writes its own evidence there, so
        // hardcoding configURL would show production's tunnel while running development.
        let url = selectedConfig
            .deletingLastPathComponent()
            .appendingPathComponent("tunnel-evidence.json")
        guard let data = try? Data(contentsOf: url),
              let decoded = try? JSONDecoder().decode(TunnelEvidence.self, from: data) else {
            // Absent or unreadable means "nothing observed", not "keep showing the last
            // value" -- a stale green indicator is worse than an honest inactive one.
            tunnelEvidence = TunnelEvidence()
            return
        }
        tunnelEvidence = decoded
    }

    /// Wake a sleeping peer: the coordinator relays a magic packet through an
    /// awake neXal Mac on the peer's network (and this Mac sends one too when it
    /// shares that network).
    func wake(_ peer: ConnectorStatus.MeshPeer) async {
        guard let tunnel = peer.tunnelAddress else { return }
        wakeStatus[peer.id] = "Sending wake request\u{2026}"
        struct Reply: Decodable { let relays: Int; let targetWakeForNetwork: Bool }
        do {
            let reply = try JSONDecoder().decode(Reply.self, from: try await invoke(.wake(tunnelAddress: tunnel)))
            let senders = reply.relays
            guard senders > 0 else {
                wakeStatus[peer.id] = "No other neXal computer on that computer\u{2019}s local network is awake to send the wake packet."
                return
            }
            var text = "Wake packet sent by \(senders) computer\(senders == 1 ? "" : "s") on its network. It can take up to 30 seconds to reconnect."
            if !reply.targetWakeForNetwork {
                text += " Wake for network access is off on that computer, so it may not wake \u{2014} open neXal@home on it once while it\u{2019}s awake to fix that."
            }
            wakeStatus[peer.id] = text
        } catch {
            // ShellError.commandFailed carries the connector's own stderr message
            // (e.g. "too many wake requests; wait a minute"), so this is friendly text.
            wakeStatus[peer.id] = error.localizedDescription
        }
    }

    /// Refresh reported selection, and retry unfinished server teardown. A saved
    /// choice is intent only; it never makes the checkbox appear selected by itself.
    func updateExitRoutes() async {
        guard !exitRouteBusy else { return }
        if let at = exitRouteCheckedAt, Date().timeIntervalSince(at) < 30 { return }
        exitRouteCheckedAt = Date()
        exitRouteBusy = true
        defer { exitRouteBusy = false }
        for (peerID, tunnel) in pendingExitTeardown {
            do {
                let reply = try JSONDecoder().decode(ExitRouteReply.self, from: try await invoke(.exitRoute(tunnelAddress: tunnel, enabled: false, targetDeviceID: peerExitDeviceIDs[peerID])))
                guard !reply.configured else { throw NetworkService.RoutingFailure.failed("Exit access removal is still pending.") }
                pendingExitTeardown.removeValue(forKey: peerID)
                exitRouteStatus[peerID] = "Exit routing is off. Temporary forwarding access was removed."
            } catch { exitRouteStatus[peerID] = "Exit routing is off locally. Retrying removal of temporary forwarding access."
            }
        }
        UserDefaults.standard.set(pendingExitTeardown, forKey: "pendingExitTeardown")
        guard let state = await Task.detached(operation: { NetworkService.exitRoutes() }).value else {
            availableExitRoutes = []; exitRoute = nil; return
        }
        availableExitRoutes = state.available
        exitRoute = state.selected.sorted().first
        if let chosen = desiredExitRoute, state.available.contains(chosen), !state.selected.contains(chosen) {
            do {
                let previous = exitRoute
                try await Task.detached { try NetworkService.selectExitRoute(chosen, previous: previous) }.value
                exitRoute = chosen
            } catch { message = "Could not restore the exit route: \(error.localizedDescription)" }
        }
    }

    private struct ExitRouteReply: Decodable { let routeId: String; let configured: Bool; let targetDeviceId: String? }

    func setPeerExitRoute(_ peer: ConnectorStatus.MeshPeer, storageGateway: Bool, enabled: Bool) async {
        guard !exitRouteBusy else { return }
        exitRouteBusy = true
        defer { exitRouteBusy = false }
        exitRouteStatus[peer.id] = enabled ? "Preparing this exit node…" : "Turning off exit routing…"
        do {
            let previous = exitRoute
            var route = storageGateway ? NetworkService.storageExitRoute : peerExitRoutes[peer.id]
            if enabled, !storageGateway {
                guard let tunnel = peer.tunnelAddress else { throw NetworkService.RoutingFailure.failed("This computer does not have a neXal network address yet.") }
                let reply = try JSONDecoder().decode(ExitRouteReply.self, from: try await invoke(.exitRoute(tunnelAddress: tunnel, enabled: true)))
                guard reply.configured, NetworkService.validExitRouteID(reply.routeId) else { throw NetworkService.RoutingFailure.failed("neXal could not set up this exit route. Try again shortly.") }
                route = reply.routeId
                peerExitRoutes[peer.id] = reply.routeId
                if let targetID = reply.targetDeviceId { peerExitDeviceIDs[peer.id] = targetID }
                UserDefaults.standard.set(peerExitDeviceIDs, forKey: "peerExitDeviceIDs")
                UserDefaults.standard.set(peerExitRoutes, forKey: "peerExitRouteIDs")
                pendingExitTeardown.removeValue(forKey: peer.id)
                UserDefaults.standard.set(pendingExitTeardown, forKey: "pendingExitTeardown")
            }
            guard let route else { throw NetworkService.RoutingFailure.failed("No exit route is set up for this computer.") }
            if enabled {
                exitRouteStatus[peer.id] = "Waiting for the network service to receive this route…"
                var offered = false
                for _ in 0..<20 {
                    if let state = await Task.detached(operation: { NetworkService.exitRoutes() }).value {
                        availableExitRoutes = state.available
                        if state.available.contains(route) { offered = true; break }
                    }
                    try await Task.sleep(nanoseconds: 1_000_000_000)
                }
                guard offered else { throw NetworkService.RoutingFailure.failed("The route was configured, but has not reached this Mac. Try again shortly.") }
            }
            try await Task.detached { try NetworkService.selectExitRoute(enabled ? route : nil, previous: enabled ? previous : route) }.value
            desiredExitRoute = enabled ? route : nil
            exitRoute = enabled ? route : nil
            UserDefaults.standard.set(desiredExitRoute, forKey: Self.exitRouteKey)
            if enabled, let previous, previous != route,
               let previousPeerID = peerExitRoutes.first(where: { $0.value == previous })?.key,
               let previousPeer = status?.mesh?.peers.first(where: { $0.id == previousPeerID }),
               let tunnel = previousPeer.tunnelAddress {
                // Selecting another peer also unchecks the former peer; its
                // temporary forwarding permission must be removed as well.
                pendingExitTeardown[previousPeerID] = tunnel
                UserDefaults.standard.set(pendingExitTeardown, forKey: "pendingExitTeardown")
                do {
                    let oldReply = try JSONDecoder().decode(ExitRouteReply.self, from: try await invoke(.exitRoute(tunnelAddress: tunnel, enabled: false, targetDeviceID: peerExitDeviceIDs[previousPeerID])))
                    if !oldReply.configured { pendingExitTeardown.removeValue(forKey: previousPeerID) }
                } catch { exitRouteStatus[previousPeerID] = "Previous exit is off locally. Temporary access cleanup will retry." }
                UserDefaults.standard.set(pendingExitTeardown, forKey: "pendingExitTeardown")
            }
            if !enabled, !storageGateway, let tunnel = peer.tunnelAddress {
                // Persist before network I/O so a temporary outage or app restart
                // cannot silently lose the request to restore default blocking.
                pendingExitTeardown[peer.id] = tunnel
                UserDefaults.standard.set(pendingExitTeardown, forKey: "pendingExitTeardown")
                let reply = try JSONDecoder().decode(ExitRouteReply.self, from: try await invoke(.exitRoute(tunnelAddress: tunnel, enabled: false, targetDeviceID: peerExitDeviceIDs[peer.id])))
                guard !reply.configured else { throw NetworkService.RoutingFailure.failed("Exit access removal has not been confirmed.") }
                pendingExitTeardown.removeValue(forKey: peer.id)
                UserDefaults.standard.set(pendingExitTeardown, forKey: "pendingExitTeardown")
                availableExitRoutes.remove(route)
            }
            exitRouteStatus[peer.id] = enabled
                ? "Your internet traffic now goes through this exit node. Internet access depends on it staying online."
                : "Exit routing is off. Normal routing and access rules are restored."
        } catch {
            if let state = await Task.detached(operation: { NetworkService.exitRoutes() }).value {
                availableExitRoutes = state.available; exitRoute = state.selected.sorted().first
            } else { exitRoute = nil }
            if enabled, !storageGateway, exitRoute != peerExitRoutes[peer.id], let tunnel = peer.tunnelAddress {
                // A server route may have been created before delivery/selection
                // failed. An unchecked box must not leave temporary access behind.
                pendingExitTeardown[peer.id] = tunnel
                UserDefaults.standard.set(pendingExitTeardown, forKey: "pendingExitTeardown")
            }
            exitRouteStatus[peer.id] = pendingExitTeardown[peer.id] != nil
                ? "Exit routing is off locally. Temporary access cleanup is pending and will retry."
                : "Could not change exit routing: \(error.localizedDescription)"
            message = exitRouteStatus[peer.id]
        }
    }

    private func updatePeerLocations() async {
        let peers = status?.mesh?.peers ?? []
        var next: [String: PeerNetInfo] = [:]
        for peer in peers where peer.directAddress != nil {
            next[peer.id] = await PeerLocator.shared.info(directAddress: peer.directAddress,
                                                         directIsPrivate: peer.directIsPrivate ?? false)
        }
        if next != peerNetInfo { peerNetInfo = next }
        let mine = await PeerLocator.shared.ownPublicIP()
        let mineInfo = PeerNetInfo(publicAddress: mine.ip, location: mine.location)
        if mineInfo != ownNetInfo { ownNetInfo = mineInfo }
    }

    private func updatePeers() async {
        guard let data = try? await invoke(.peersView),
              let decoded = try? JSONDecoder().decode(PeersView.self, from: data) else {
            // No agent, or a connector too old to serve peers-view. An empty list with
            // remoteAccessAvailable false is the honest reading; the panel explains it.
            peersView = PeersView()
            return
        }
        peersView = decoded
    }

	func updateTimeMachine(force: Bool = false) async {
        defer {
            let host = timeMachine?.timeMachine.host?.split(separator: ".").first.map(String.init)
            let connected = status?.mesh?.peers.contains {
                ($0.name.lowercased() == host?.lowercased() || $0.tunnelAddress == timeMachine?.timeMachine.host) && $0.lifecycle == "connected"
            } == true
            timeMachineDiscovery.update(timeMachine?.timeMachine, connected: connected)
        }
		if !force, let lastTimeMachineCheck, Date().timeIntervalSince(lastTimeMachineCheck) < 60 { return }
		lastTimeMachineCheck = Date()
		guard hasPersistedHostIdentity, let data = try? await invoke(.timeMachine),
		      let decoded = try? TimeMachineReport.decode(data) else {
			timeMachine = nil
			return
		}
		timeMachine = decoded
	}

	/// One-click Time Machine: adds this network's storage gateway as a backup
	/// disk. macOS shows its own administrator dialog; the backup password goes
	/// from the coordinator to tmutil without ever being shown or stored here.
    func openServiceApplication(_ bundleID: String, url: URL? = nil) {
        guard let application = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else {
            message = "The macOS app for this service could not be found."
            return
        }
        let configuration = NSWorkspace.OpenConfiguration()
        let completion: @Sendable (NSRunningApplication?, Error?) -> Void = { _, error in
            if let error {
                Task { @MainActor in self.message = "Could not open service: \(error.localizedDescription)" }
            }
        }
        if let url {
            NSWorkspace.shared.open([url], withApplicationAt: application,
                                    configuration: configuration, completionHandler: completion)
        } else {
            NSWorkspace.shared.openApplication(at: application,
                                               configuration: configuration, completionHandler: completion)
        }
    }

    func revealTimeMachineCredentials() async {
        guard !busy else { return }
        TimeMachineCredentialWindow.shared.begin()
        busy = true
        defer { busy = false }
        do {
            let data = try await invoke(.timeMachineCredentials)
            let credential = try JSONDecoder().decode(TimeMachineCredential.self, from: data)
            TimeMachineCredentialWindow.shared.show(credential)
        } catch {
            message = "Could not get Time Machine credentials. Check that backup is turned on and this Mac is online."
            TimeMachineCredentialWindow.shared.showError()
        }
    }

	func setUpTimeMachine() async {
		guard !busy else { return }
		TimeMachineSetupWindow.shared.begin()
		busy = true; activity = "Checking Time Machine access before administrator approval…"
		defer { busy = false; activity = nil }
		do {
            let result = try await invoke(.timeMachineConnect)
            struct SetupResult: Decodable {
                struct Status: Decodable { let state: String; let detail: String? }
                let timeMachine: Status
            }
            let report = try JSONDecoder().decode(SetupResult.self, from: result).timeMachine
            guard ["connected", "destination_added"].contains(report.state) else {
                throw ShellError.commandFailed(1, reason: report.detail ?? "Time Machine setup did not add a backup destination.")
            }
			message = "Backup disk added. It now appears in System Settings \u{203A} General \u{203A} Time Machine, where you can review the backup schedule."
			TimeMachineSetupWindow.shared.finish(error: nil)
		} catch {
			message = error.localizedDescription
            TimeMachineSetupWindow.shared.finish(error: error.localizedDescription)
		}
		await updateTimeMachine(force: true)
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
            message = "This Mac accepts private jobs at no cost until the time shown. Memory and connection checks still apply. Pause stops this and any running work."
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
                ? "Resource sharing is on. neXal still checks each job before it runs."
                : "Resource sharing is paused. Any running work is stopped."
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
        if guestAccess != nil || NetworkService.guestExpiryGuardInstalled {
            do { try await Task.detached(priority: .userInitiated) { try NetworkService.removeGuestExpiryGuard() }.value }
            catch { pairingProblem = error.localizedDescription; return }
        }
        if !configurationExists {
            activity = "Creating this Mac\u{2019}s configuration\u{2026}"
            do {
                if developmentEnvironment {
                    _ = try await invoke(.initializeDevelopment(name: hostName,
                                               memoryMiB: memoryMiB, reserveMiB: reserveMiB))
                } else {
                    _ = try await invoke(.initialize(coordinator: coordinator, name: hostName,
                                               memoryMiB: memoryMiB, reserveMiB: reserveMiB))
                }
            } catch {
                pairingProblem = error.localizedDescription
                return
            }
            activity = "Requesting a pairing code\u{2026}"
        }
        // Pairing ends with this Mac joining the secure network, which needs the
        // root networking service. Install it BEFORE showing a code: finding out
        // after the phone has scanned leaves both devices stuck on "joining".
        if !NetworkService.isRunning {
            activity = "Installing the secure networking service\u{2026}"
            do { try await Task.detached(priority: .userInitiated) { try NetworkService.install() }.value }
            catch { pairingProblem = error.localizedDescription; return }
            activity = "Requesting a pairing code\u{2026}"
        }
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
            message = "Pairing code ready. In neXal@home, choose \u{201C}Pair a computer\u{201D} and scan it. The code expires shortly, and scanning alone shares nothing."
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

    func resetUnfinishedPairing() async {
        guard !busy, hasUnfinishedEnrollment else { return }
        busy = true; activity = "Clearing the failed pairing…"
        defer { busy = false; activity = nil }
        do {
            _ = try await invoke(.resetLocalPairing)
            pairingWatch?.cancel(); pairingWatch = nil
            pairing = nil; status = nil; lastUpdated = nil; pairingProblem = nil
            message = "The failed pairing was cleared. Generate a new code and scan it again."
        } catch { message = error.localizedDescription }
    }

    /// Poll this pairing's status through the CLI until it stops being live.
    /// True when the networking service is absent or stopped. Every neXal Mac
    /// needs it, so the panel offers the install whenever it is missing.
    var needsNetworkService: Bool {
        !NetworkService.isRunning && (try? NetworkService.validatedHelper()) != nil
    }

    /// Install the networking service for a Mac that is already enrolling (the
    /// case where pairing started before this app installed it). The next status
    /// poll re-runs the join with the saved credential.
    func installNetworkService() async {
        guard !busy else { return }
        busy = true
        activity = "Installing the secure networking service\u{2026}"
        do {
            try await Task.detached(priority: .userInitiated) { try NetworkService.install() }.value
            pairingProblem = nil
            message = "Secure networking service installed. Finishing the join\u{2026}"
        } catch {
            pairingProblem = error.localizedDescription
        }
        // Cleared before refreshing: refresh() does nothing while busy.
        busy = false
        activity = nil
        await rejoinNetworkIfEnrolled()
        await refresh()
    }

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
                message = "Your iPhone scanned the code. Finish on your iPhone; nothing is shared until you approve it there."
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
