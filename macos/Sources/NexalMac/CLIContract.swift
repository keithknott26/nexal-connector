import Foundation

/// Thin adapter to connector/CLI-CONTRACT.md v1. No HTTP, credentials,
/// scheduling, enrollment policy, or resource accounting is implemented here.
enum CLICommand {
    case initialize(coordinator: String, name: String, memoryMiB: Int, reserveMiB: Int)
    case initializeLocalPreview(name: String, memoryMiB: Int, reserveMiB: Int)
    case enroll, run, status, pause, resume, acceptJobs
    /// Mint a phone pairing and render it. `--no-poll` is deliberate: this app
    /// polls with `pairingStatus` through the SAME bounded `ConnectorProcess`
    /// execution as every other command, rather than holding a long-lived child
    /// process open and parsing a stream. One process per question keeps the
    /// 20-second bound, the 64 KiB output cap and the single-seam rule intact.
    case pair(role: PairingRole)
    case pairingStatus(pairingId: String)
    case cancelPairing(pairingId: String)

    func arguments(config: URL) -> [String] {
        let command: [String]
        switch self {
        case let .initialize(coordinator, name, memoryMiB, reserveMiB):
            command = ["init", "--coordinator", coordinator, "--name", name,
                       "--memory-limit-mib", String(memoryMiB),
                       "--reserve-memory-mib", String(reserveMiB)]
        case let .initializeLocalPreview(name, memoryMiB, reserveMiB):
            command = ["init", "--coordinator", "http://127.0.0.1:8787", "--name", name,
                       "--memory-limit-mib", String(memoryMiB),
                       "--reserve-memory-mib", String(reserveMiB),
                       "--dev-loopback", "--dev-secrets"]
        case .enroll: command = ["enroll", "--code-stdin"]
        case .run: command = ["run"]
        case .status: command = ["status"]
        case .pause: command = ["pause"]
        case .resume: command = ["resume"]
        case .acceptJobs: command = ["accept-jobs"]
        case let .pair(role):
            command = ["pair", "--role", role.rawValue, "--no-poll"]
        case let .pairingStatus(pairingId):
            command = ["pair", "--status", pairingId]
        case let .cancelPairing(pairingId):
            command = ["pair", "--cancel", pairingId]
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
    case rejectedExecutable, executableChanged, commandFailed(Int32), timeout, oversizedOutput
    case invalidStatus, noExecutable, invalidCode, invalidPairing

    var errorDescription: String? {
        switch self {
        case .rejectedExecutable:
            return "Choose the nexal executable in this app’s Helpers folder or ~/Library/Application Support/Nexal/bin. It must not be a symlink or writable by other users."
        case .executableChanged:
            return "The selected connector changed. Review its signature and choose it again."
        case .commandFailed(let code):
            return "The Go connector rejected this operation (exit \(code)). Check the coordinator, invitation, and local configuration. Credentials are not displayed."
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
    let resourcePolicy: ResourcePolicy?

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
        do { return try JSONDecoder().decode(Self.self, from: firstLine(of: data)) }
        catch { throw ShellError.invalidPairing }
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
        do { return try JSONDecoder().decode(Self.self, from: firstLine(of: data)) }
        catch { throw ShellError.invalidPairing }
    }
}

/// The CLI emits one JSON object per line, and a future connector may emit more
/// than one for a command this app treats as one-shot. Taking the first line is
/// forward-compatible; concatenating them would produce invalid JSON and blank
/// the panel instead of degrading it.
private func firstLine(of data: Data) -> Data {
    guard let newline = data.firstIndex(of: 0x0A) else { return data }
    return data[data.startIndex..<newline]
}
