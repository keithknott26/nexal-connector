import CryptoKit
import Darwin
import Foundation

struct ExecutableSelection: Sendable {
    let url: URL
    let sha256: String

    static var supportDirectory: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/Nexal", isDirectory: true)
    }

    static var bundledExecutable: URL {
        Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/nexal")
    }

    static func approve(_ chosen: URL) throws -> ExecutableSelection {
        let fm = FileManager.default
        let path = chosen.standardizedFileURL
        let allowed = [
            bundledExecutable.standardizedFileURL,
            supportDirectory.appendingPathComponent("bin/nexal").standardizedFileURL
        ]
        guard allowed.contains(path), path == path.resolvingSymlinksInPath(),
              fm.isExecutableFile(atPath: path.path) else {
            throw ShellError.rejectedExecutable
        }
        let attrs = try fm.attributesOfItem(atPath: path.path)
        let permissions = (attrs[.posixPermissions] as? NSNumber)?.intValue ?? 0
        let owner = (attrs[.ownerAccountID] as? NSNumber)?.uint32Value
        let size = (attrs[.size] as? NSNumber)?.uint64Value ?? 0
        guard attrs[.type] as? FileAttributeType == .typeRegular,
              permissions & 0o022 == 0, owner == getuid() || owner == 0,
              size > 0, size <= 128 * 1024 * 1024 else {
            throw ShellError.rejectedExecutable
        }
        // Pin the binary chosen by the owner; do not silently discover a PATH binary.
        let bytes = try Data(contentsOf: path, options: .mappedIfSafe)
        let hash = SHA256.hash(data: bytes).map { String(format: "%02x", $0) }.joined()
        return ExecutableSelection(url: path, sha256: hash)
    }

    func revalidate() throws {
        guard try Self.approve(url).sha256 == sha256 else {
            throw ShellError.executableChanged
        }
    }
}

private final class BoundedCapture: @unchecked Sendable {
    private let lock = NSLock()
    private var storage = Data()
    private var exceeded = false

    func drain(_ handle: FileHandle) {
        while true {
            let data = handle.availableData
            if data.isEmpty { break }
            lock.lock()
            if storage.count + data.count <= 65_536 { storage.append(data) }
            else { exceeded = true } // Continue draining, preventing pipe deadlock.
            lock.unlock()
        }
    }

    func result() throws -> Data {
        lock.lock(); defer { lock.unlock() }
        if exceeded { throw ShellError.oversizedOutput }
        return storage
    }

    /// Whatever was captured, without the oversize check. Used only for the
    /// stderr envelope on a failure path, where a truncated buffer should still
    /// be parsed for a diagnosis rather than replaced with a generic message.
    func bytes() -> Data {
        lock.lock(); defer { lock.unlock() }
        return storage
    }
}

/// Decodes the connector's structured error envelope.
///
/// The Go CLI writes `{"error":{"code":...,"message":...}}` to stderr on every
/// failure, and that message is written for the owner to read. This type exists
/// so the ONLY thing that can reach the interface is that one decoded field:
/// raw stderr is never surfaced, so unstructured output cannot leak through.
// A caseless namespace, deliberately: it decodes an envelope, it is never itself
// decoded, and declaring a Decodable conformance with no cases would not compile.
enum ConnectorFailure {
    private struct Envelope: Decodable {
        struct Inner: Decodable { let code: String?; let message: String? }
        let error: Inner
    }

    /// The connector's own explanation, or nil when stderr was absent, not JSON,
    /// or carried no usable message -- in which case the caller falls back to the
    /// generic exit-code wording rather than showing an empty string.
    static func reason(in stderr: Data) -> String? {
        guard !stderr.isEmpty,
              let envelope = try? JSONDecoder().decode(Envelope.self, from: stderr),
              let message = envelope.error.message else { return nil }
        // Control characters are stripped and the length is bounded, so a hostile
        // or malformed message cannot reflow or flood the panel.
        let cleaned = message
            .components(separatedBy: .controlCharacters).joined(separator: " ")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleaned.isEmpty else { return nil }
        return cleaned.count <= 400 ? cleaned : String(cleaned.prefix(400)) + "\u{2026}"
    }
}

enum ConnectorProcess {
    static var configURL: URL {
        ExecutableSelection.supportDirectory.appendingPathComponent("config.json")
    }

    /// Whether pairing has durably issued this Mac a coordinator host identity.
    /// Reading this one non-secret field lets the menu app distinguish the normal
    /// pre-pairing state (where no local agent should be listening yet) from an
    /// enrolled connector that has stopped and should be restarted.
    static func hasPersistedHostIdentity(at url: URL) -> Bool {
        struct Identity: Decodable { let hostId: String? }
        guard let data = try? Data(contentsOf: url),
              let value = try? JSONDecoder().decode(Identity.self, from: data),
              let hostId = value.hostId?.trimmingCharacters(in: .whitespacesAndNewlines) else {
            return false
        }
        return !hostId.isEmpty
    }

    static func hasUnfinishedEnrollment(at url: URL) -> Bool {
        struct Stored: Decodable {
            struct Enrollment: Decodable { let status: String }
            let enrollment: Enrollment?
        }
        guard let data = try? Data(contentsOf: url),
              let value = try? JSONDecoder().decode(Stored.self, from: data) else { return false }
        return value.enrollment.map { $0.status == "joining" || $0.status == "provisioning" } ?? false
    }

    /// The development profile's configuration, kept in its own directory so a
    /// development identity and credentials can never be mistaken for the real
    /// ones. CLIContractTests asserts this is not equal to `configURL`.
    ///
    /// The directory is renamed from Nexal-Local-Preview, and the rename matters
    /// beyond tidiness: an existing config.json under the old name would make
    /// `configurationExists` true, so enrollment would skip `init` entirely and
    /// keep using a configuration still pointing at http://127.0.0.1:8787 --
    /// exactly the dead loopback port this change exists to stop using. A new
    /// path means the development profile is initialized once, correctly, against
    /// the hosted coordinator.
    static var developmentConfigURL: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/Nexal-Development/config.json")
    }

    static func make(_ selection: ExecutableSelection, _ command: CLICommand,
                     config: URL? = nil) throws -> Process {
        try selection.revalidate()
        let process = Process()
        process.executableURL = selection.url
        process.arguments = command.arguments(config: config ?? configURL)
        // No inherited DYLD/PYTHON/Go injection, no enrollment secrets in env.
        process.environment = [
            "HOME": FileManager.default.homeDirectoryForCurrentUser.path,
            "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
            "TMPDIR": NSTemporaryDirectory(),
            "LANG": "en_US.UTF-8"
        ]
        process.currentDirectoryURL = FileManager.default.homeDirectoryForCurrentUser
        return process
    }

    /// Run only on a worker queue. Never invokes /bin/sh or evaluates a command string.
    static func execute(
        _ selection: ExecutableSelection, _ command: CLICommand, stdin: Data? = nil,
        config: URL? = nil
    ) throws -> Data {
        let process = try make(selection, command, config: config)
        let output = Pipe()
        let errorOutput = Pipe()
        let input = Pipe()
        process.standardOutput = output
        process.standardError = errorOutput
        process.standardInput = input
        let stdout = BoundedCapture()
        let stderr = BoundedCapture()
        let group = DispatchGroup()
        try process.run()
        for (capture, handle) in [
            (stdout, output.fileHandleForReading), (stderr, errorOutput.fileHandleForReading)
        ] {
            group.enter()
            DispatchQueue.global(qos: .utility).async {
                capture.drain(handle)
                group.leave()
            }
        }
        let deadline = DispatchWorkItem {
            if process.isRunning {
                process.terminate()
                DispatchQueue.global().asyncAfter(deadline: .now() + 2) {
                    if process.isRunning { Darwin.kill(process.processIdentifier, SIGKILL) }
                }
            }
        }
        DispatchQueue.global().asyncAfter(deadline: .now() + 20, execute: deadline)
        let started = Date()
        do {
            if let stdin { try input.fileHandleForWriting.write(contentsOf: stdin) }
            try input.fileHandleForWriting.close()
        } catch {
            if process.isRunning { process.terminate() }
            deadline.cancel()
            throw error
        }
        process.waitUntilExit()
        deadline.cancel()
        guard group.wait(timeout: .now() + 3) == .success else { throw ShellError.timeout }
        if Date().timeIntervalSince(started) >= 20 { throw ShellError.timeout }
        guard process.terminationStatus == 0 else {
            // Raw stderr is still never displayed or persisted. What IS surfaced is
            // the `message` field of the connector's structured error envelope,
            // which cmd/nexal/main.go writes on every failure:
            //
            //     {"error":{"code":"connector_error","message":"..."}}
            //
            // Discarding it was actively harmful. The Go side already says exactly
            // what is wrong -- "this Mac is not enrolled, so it has no host
            // credential to pair with; run nexal enroll first" -- and the panel
            // replaced that with "check the coordinator, invitation, and local
            // configuration", which names three subsystems and points at none of
            // them. Every failure looked identical, so no owner could act on one.
            //
            // Only the decoded field is read, never the raw bytes, so unstructured
            // output -- a Go panic, a dyld message, anything a future build writes
            // -- still cannot reach the interface.
            throw ShellError.commandFailed(process.terminationStatus,
                                           reason: ConnectorFailure.reason(in: stderr.bytes()))
        }
        // Stderr is intentionally never displayed or persisted on success.
        return try stdout.result()
    }
}
