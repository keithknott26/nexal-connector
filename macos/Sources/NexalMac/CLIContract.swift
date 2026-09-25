import Foundation

/// The coordinator origins this app knows by name.
///
/// Declared here, outside the @MainActor model, because `CLICommand.arguments`
/// builds an argv from a nonisolated context and must be able to read them
/// without hopping to the main actor.
enum CoordinatorOrigins {
    /// The deployed development environment. Development is a hosted environment
    /// now, not a server the owner starts on their own machine.
    static let development = "https://coordinator-dev.nexal.systems"
    /// The production environment.
    static let production = "https://coordinator.nexal.systems"
}

/// Thin adapter to connector/CLI-CONTRACT.md v1. No HTTP, credentials,
/// scheduling, enrollment policy, or resource accounting is implemented here.
enum CLICommand {
    case initialize(coordinator: String, name: String, memoryMiB: Int, reserveMiB: Int)
    /// Initialize against the hosted DEVELOPMENT coordinator.
    ///
    /// This used to target http://127.0.0.1:8787, a coordinator the owner had to
    /// run themselves. That local server is no longer how development works --
    /// coordinator-dev.nexal.systems is deployed -- and pointing at a port nobody
    /// is listening on made the option fail for a reason the panel could not
    /// explain. It also forced the "cannot pair" caveat, because pairing requires
    /// an https origin and a loopback http origin can never satisfy it.
    case initializeDevelopment(name: String, memoryMiB: Int, reserveMiB: Int)
    case enroll, run, status, pause, resume, acceptJobs
    /// The live peer view. Named peers-view in the CLI because `peers` is invitation
    /// and enrollment management, which reports no addresses.
    case peersView
	case timeMachine
    /// Mint a phone pairing and render it. `--no-poll` is deliberate: this app
    /// polls with `pairingStatus` through the SAME bounded `ConnectorProcess`
    /// execution as every other command, rather than holding a long-lived child
    /// process open and parsing a stream. One process per question keeps the
    /// 20-second bound, the 64 KiB output cap and the single-seam rule intact.
    case pair(role: PairingRole)
    case pairingStatus(pairingId: String)
    case cancelPairing(pairingId: String)
    case resetLocalPairing
    case leaveNetwork
    /// Reconnect an already-enrolled Mac to its secure network (after a
    /// reinstall, service restart or reboot). Harmless when not enrolled.
    case rejoinNetwork

    func arguments(config: URL) -> [String] {
        let command: [String]
        switch self {
        case let .initialize(coordinator, name, memoryMiB, reserveMiB):
            command = ["init", "--coordinator", coordinator, "--name", name,
                       "--memory-limit-mib", String(memoryMiB),
                       "--reserve-memory-mib", String(reserveMiB)]
        case let .initializeDevelopment(name, memoryMiB, reserveMiB):
            // --dev-loopback and --dev-secrets are retained deliberately. They are
            // what keep this profile from doing public work or spending, and what
            // route credentials to restricted-permission files instead of the
            // Keychain, so a development identity is never mistaken for the real
            // one. Only the coordinator changed, from an unhosted loopback port to
            // the deployed development environment.
            command = ["init", "--coordinator", CoordinatorOrigins.development, "--name", name,
                       "--memory-limit-mib", String(memoryMiB),
                       "--reserve-memory-mib", String(reserveMiB),
                       "--dev-loopback", "--dev-secrets"]
        case .enroll: command = ["enroll", "--code-stdin"]
        case .run: command = ["run"]
        case .status: command = ["status"]
        case .peersView: command = ["peers-view"]
		case .timeMachine: command = ["time-machine"]
        case .pause: command = ["pause"]
        case .resume: command = ["resume"]
        case .acceptJobs: command = ["accept-jobs"]
        case let .pair(role):
            _ = role // Compute role remains a policy chosen after network enrollment.
            command = ["pair-v2", "--create"]
        case let .pairingStatus(pairingId):
            command = ["pair-v2", "--status", pairingId]
        case let .cancelPairing(pairingId):
            command = ["pair-v2", "--cancel", pairingId]
        case .resetLocalPairing:
            command = ["pair-v2", "--reset-local"]
        case .leaveNetwork:
            command = ["pair-v2", "--leave"]
        case .rejoinNetwork:
            command = ["pair-v2", "--rejoin"]
        }
        return command + ["--config", config.path]
    }
}

/// The two roles the coordinator and the phone both accept, and nothing else.
/// A raw string is used because it is passed straight to the Go CLI as an
/// argument; an unconstrained String there would let a typo become a coordinator
/// request that fails for a reason the owner cannot see.
enum PairingRole: String, CaseIterable, Identifiable {
    case receiver, donor

    var id: String { rawValue }

    /// Written for someone who has never read the protocol: the role decides
    /// which side of a transfer this Mac is, and getting it wrong is the kind of
    /// mistake that is only visible after the phone has already scanned.
    var label: String {
        switch self {
        case .receiver: return "This Mac receives"
        case .donor: return "This Mac donates"
        }
    }

    var explanation: String {
        switch self {
        case .receiver: return "The phone pairs this Mac as the device that receives resources from your other devices."
        case .donor: return "The phone pairs this Mac as the device that donates its resources to your other devices."
        }
    }
}

enum ShellError: LocalizedError {
    case rejectedExecutable, executableChanged, timeout, oversizedOutput
    /// `reason` is the connector's own message, decoded from its structured error
    /// envelope. Nil when stderr carried nothing usable, which is the only case
    /// that still falls back to naming the exit code.
    case commandFailed(Int32, reason: String?)
    case invalidStatus, noExecutable, invalidCode, invalidPairing

    var errorDescription: String? {
        switch self {
        case .rejectedExecutable:
            return "Choose the nexal executable in this app’s Helpers folder or ~/Library/Application Support/Nexal/bin. It must not be a symlink or writable by other users."
        case .executableChanged:
            return "The selected connector changed. Review its signature and choose it again."
        case let .commandFailed(code, reason):
            // Prefer the connector's own words. It knows which precondition failed;
            // this app can only guess, and its guess used to name three subsystems
            // at once while pointing at none of them.
            if let reason { return reason }
            return "The connector could not complete this operation (exit \(code)) and gave no reason. Credentials are never displayed."
        case .timeout: return "The connector did not respond before the timeout."
        case .oversizedOutput: return "The connector returned too much data."
        case .invalidStatus: return "The connector status schema is not supported. Update the app and connector together."
        case .noExecutable: return "Choose an installed Go connector before continuing."
        case .invalidCode: return "Enter a one-use enrollment code (at most 255 bytes)."
        case .invalidPairing:
            return "The connector returned a pairing this app cannot display. Update the app and connector together; no code is shown rather than showing one that may not scan."
        }
    }
}

struct TimeMachineReport: Decodable {
	struct State: Decodable {
		let state: String; let enabled: Bool; let entitled: Bool
		let capacityBytes: UInt64?; let freeBytes: UInt64?; let shareName: String?
		let advertised: Bool; let detailCode: String?
	}
	let timeMachine: State
	let action: String?
	static func decode(_ data: Data) throws -> Self {
		do { return try JSONDecoder().decode(Self.self, from: data) }
		catch { throw ShellError.invalidStatus }
	}
}

struct ConnectorStatus: Decodable {
    let paused: Bool
    let name: String?
    let hostId: String?
    let version: String?
    let marketplaceEnabled: Bool?
    let mode: String?
    let transport: String?
    let productionDispatchVerified: Bool?
    let activeAttempt: String?
    let telemetry: Telemetry?
    let manualAcceptanceSupported: Bool?
    let ownerActivityOverride: Bool?
    let acceptJobsUntil: String?
    let executionBlocker: String?
    let lastOutcome: String?
    let coordinatorHealthy: Bool?
    /// The coordinator answers this Mac's heartbeat with 401: its credential was
    /// revoked or expired. Absent from older connectors.
    let credentialRejected: Bool?
    let resourcePolicy: ResourcePolicy?
	let mesh: MeshStatus?

	struct MeshStatus: Decodable {
		let providerAvailable: Bool
		let lifecycle: String
		let authenticationStep: String?
		let pq: String
		let updatedAt: String?
		let peers: [MeshPeer]
		let discovery: MeshDiscovery?
	}
	struct MeshDiscovery: Decodable {
		let wideAreaBonjour: Bool; let gateway: String; let bridge: String
		let siteId: String?; let lastRecordAt: String?; let detail: String?
	}
	struct MeshPeer: Decodable, Identifiable {
		let id: String; let name: String; let lifecycle: String
		let authenticationStep: String?; let path: String; let pathLabel: String
		let relayRegion: String?; let latencyMs: Double?; let packetLossPercent: Double?
		let lastHandshakeAt: String?; let pq: String; let pqVerifiedAt: String?
		let traffic: MeshTraffic
		let fileSharing: MeshFileSharing?
		let screenSharing: MeshScreenSharing?
		let hostname: MeshHostname?
	}
	struct MeshTraffic: Decodable { let receivedBytes: UInt64; let sentBytes: UInt64; let lastAt: String? }
	struct MeshFileSharing: Decodable {
		let authorized: Bool; let available: Bool; let address: String?
		let shareName: String?; let detail: String?
	}
	struct MeshScreenSharing: Decodable {
		let authorized: Bool; let available: Bool; let address: String?; let detail: String?
	}
	struct MeshHostname: Decodable { let state: String; let hostname: String?; let detail: String? }

    struct Telemetry: Decodable {
        let known: Bool
        let synthetic: Bool
        let ownerActive: Bool
        // Reported by the Go agent today, optional here so an older connector
        // that omits them still decodes instead of failing the whole status.
        // availableMemoryBytes was briefly required, which meant one missing
        // memory reading threw invalidStatus and discarded the ENTIRE status —
        // including `paused`, the consent signal. A chart gap must never be able
        // to take down consent display. Absent stays absent; mib() maps nil to
        // nil, so it is still never charted as a measured zero.
        let availableMemoryBytes: UInt64?
        let idleSeconds: UInt64?
        let totalMemoryBytes: UInt64?
    }

    /// The connector's own approved resource policy. Optional throughout: a
    /// missing value is charted as absent, never as a measured zero.
    struct ResourcePolicy: Decodable {
        let memoryLimitBytes: UInt64?
        let reserveMemoryBytes: UInt64?
        let idleSeconds: UInt64?
    }

    // Decode only stable public status fields. Unknown fields are ignored;
    // a missing paused field is an error, never interpreted as consent.
    static func decode(_ data: Data) throws -> ConnectorStatus {
        do { return try JSONDecoder().decode(Self.self, from: data) }
        catch { throw ShellError.invalidStatus }
    }
}

/// The `nexal pair --role … --no-poll` record, decoded from the CLI's single
/// stdout line.
///
/// NOTE what is absent: the claim token. The Go CLI emits a `claimToken` field
/// whose value is a sentence explaining that the secret is NOT there, and this
/// type deliberately does not decode it. The token authorizes a phone to claim
/// this Mac; it exists in the connector's memory and inside the QR modules, and
/// it must never enter this process, its logs, or a crash report. The modules
/// themselves carry it — that is unavoidable, since they are the code the phone
/// reads — which is why they are drawn and never written to disk here.
struct PairingMint: Decodable {
    let pairing: Pairing

    struct Pairing: Decodable {
        let pairingId: String
        let role: String
        let coordinator: String
        let expiresAt: String
        let status: String
        let qr: Symbol
		let manualCode: String?

        struct Symbol: Decodable {
            let version: Int
            let mask: Int
            let size: Int
            let quietZone: Int
            let errorLevel: String
            let encoding: String
            /// One string per row, "1" for a dark module. The Go side is the only
            /// QR encoder in this system; if the app had its own, the two could
            /// drift and only a phone would ever notice.
            let moduleRows: [String]
        }
    }

    static func decode(_ data: Data) throws -> PairingMint {
        do {
            let line = firstLine(of: data)
            if let current = try? JSONDecoder().decode(Self.self, from: line) { return current }
            let v2 = try JSONDecoder().decode(V2Envelope.self, from: line)
            return PairingMint(pairing: Pairing(pairingId: v2.enrollment.sessionId,
                role: "receiver", coordinator: CoordinatorOrigins.production,
                expiresAt: v2.enrollment.expiresAt, status: v2.enrollment.status,
                qr: v2.enrollment.qr, manualCode: v2.enrollment.manualCode))
        }
        catch { throw ShellError.invalidPairing }
    }

	private struct V2Envelope: Decodable {
		let enrollment: V2
		struct V2: Decodable {
			let schemaVersion: Int; let sessionId: String; let manualCode: String
			let expiresAt: String; let status: String; let step: String; let qr: Pairing.Symbol
		}
	}
}

/// The `nexal pair --status <id>` record.
struct PairingStatusReport: Decodable {
    let pairingStatus: State

    struct State: Decodable {
        let pairingId: String
        let status: String
        let expiresAt: String
    }

    static func decode(_ data: Data) throws -> PairingStatusReport {
        do {
			let line = firstLine(of: data)
			if let current = try? JSONDecoder().decode(Self.self, from: line) { return current }
			let v2 = try JSONDecoder().decode(V2Envelope.self, from: line).enrollmentStatus
			let mapped = v2.status == "paired" ? "scanned" : (["cancelled", "expired"].contains(v2.status) ? v2.status : "waiting")
			return PairingStatusReport(pairingStatus: State(pairingId: v2.sessionId, status: mapped, expiresAt: v2.expiresAt))
		}
        catch { throw ShellError.invalidPairing }
    }
	private struct V2Envelope: Decodable { let enrollmentStatus: V2; struct V2: Decodable { let sessionId: String; let status: String; let step: String; let expiresAt: String } }
}

/// The CLI emits one JSON object per line, and a future connector may emit more
/// than one for a command this app treats as one-shot. Taking the first line is
/// forward-compatible; concatenating them would produce invalid JSON and blank
/// the panel instead of degrading it.
private func firstLine(of data: Data) -> Data {
    guard let newline = data.firstIndex(of: 0x0A) else { return data }
    return data[data.startIndex..<newline]
}
