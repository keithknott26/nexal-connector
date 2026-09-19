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
}

enum ConnectorProcess {
    static var configURL: URL {
        ExecutableSelection.supportDirectory.appendingPathComponent("config.json")
    }

    static var localPreviewConfigURL: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/Nexal-Local-Preview/config.json")
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
            throw ShellError.commandFailed(process.terminationStatus)
        }
        // Stderr is intentionally never displayed or persisted, even on failures.
        return try stdout.result()
    }
}
