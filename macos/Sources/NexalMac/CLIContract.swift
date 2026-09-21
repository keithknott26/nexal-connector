import Foundation

/// Thin adapter to connector/CLI-CONTRACT.md v1. No HTTP, credentials,
/// scheduling, enrollment policy, or resource accounting is implemented here.
enum CLICommand {
    case initialize(coordinator: String, name: String, memoryMiB: Int, reserveMiB: Int)
    case initializeLocalPreview(name: String, memoryMiB: Int, reserveMiB: Int)
    case enroll, run, status, pause, resume, acceptJobs

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
        }
        return command + ["--config", config.path]
    }
}

enum ShellError: LocalizedError {
    case rejectedExecutable, executableChanged, commandFailed(Int32), timeout, oversizedOutput
    case invalidStatus, noExecutable, invalidCode

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
