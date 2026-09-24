import Foundation

/// The root-owned background service the bundled secure-networking runtime
/// (`Contents/Helpers/nexal-network`) talks to. `nexal-network up` only asks that
/// service to join; without it every join fails with "rejected startup" and the
/// coordinator never sees a connected tunnel, so the phone waits forever.
///
/// Installing it needs administrator rights once. This app does that with a
/// single macOS password prompt, and ONLY for the helper inside its own bundle:
/// nothing chosen by the user, found on PATH, or elsewhere on disk is ever run
/// as root.
enum NetworkService {
    /// The runtime's control socket, present only while the service is running.
    static let socketPath = "/var/run/netbird.sock"

    static var isRunning: Bool { FileManager.default.fileExists(atPath: socketPath) }

    static var bundledHelper: URL {
        Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/nexal-network")
    }

    enum Failure: LocalizedError {
        case helperMissing, helperUnsafe, cancelled, failed(String), didNotStart
        var errorDescription: String? {
            switch self {
            case .helperMissing:
                return "The secure networking runtime is missing from this app. Reinstall neXal Connector."
            case .helperUnsafe:
                return "The secure networking runtime in this app is a link or writable by other users, so it was not run as administrator. Reinstall neXal Connector."
            case .cancelled:
                return "The secure networking service was not installed because the administrator prompt was cancelled. This Mac cannot join until it is."
            case .failed(let detail):
                return "Installing the secure networking service failed: \(detail)"
            case .didNotStart:
                return "The secure networking service was installed but did not start within 15 seconds. Restart this Mac, then try again."
            }
        }
    }

    /// Refuse anything that is not a plain executable owned by the bundle: the
    /// same bar the Go connector applies before it will run this runtime.
    static func validatedHelper(_ url: URL = bundledHelper) throws -> URL {
        let attributes: [FileAttributeKey: Any]
        do { attributes = try FileManager.default.attributesOfItem(atPath: url.path) }
        catch { throw Failure.helperMissing }
        guard attributes[.type] as? FileAttributeType == .typeRegular,
              let perms = (attributes[.posixPermissions] as? NSNumber)?.intValue,
              perms & 0o111 != 0, perms & 0o022 == 0
        else { throw Failure.helperUnsafe }
        return url
    }

    /// Blocking: shows the macOS administrator prompt, installs and starts the
    /// service, then waits for its socket. Call off the main actor.
    static func install() throws {
        let helper = try validatedHelper()
        // The path goes in as an argument and is quoted by AppleScript itself,
        // so a space or quote in the app's location cannot become shell syntax.
        // `service install` fails harmlessly if the service is already
        // registered but stopped; `service start` is what must succeed.
        let script = [
            "on run argv",
            "set p to quoted form of (item 1 of argv)",
            // The runtime receives the encrypted tunnel on its own UDP socket, so
            // the macOS application firewall must allow it inbound. Left blocked,
            // peers can never reach this Mac directly and every connection falls
            // back to a relay (or fails). Allowing it is scoped to this binary.
            "set fw to \"/usr/libexec/ApplicationFirewall/socketfilterfw\"",
            "do shell script p & \" service install >/dev/null 2>&1; \" "
                + "& fw & \" --add \" & p & \" >/dev/null 2>&1; \" "
                + "& fw & \" --unblockapp \" & p & \" >/dev/null 2>&1; \" "
                + "& p & \" service start\" "
                + "with prompt \"neXal needs to install its secure networking service to join your network.\" "
                + "with administrator privileges",
            "end run",
        ]
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        process.arguments = script.flatMap { ["-e", $0] } + [helper.path]
        let errors = Pipe()
        process.standardError = errors
        process.standardOutput = FileHandle.nullDevice
        try process.run()
        process.waitUntilExit()
        if process.terminationStatus != 0 {
            let text = String(decoding: errors.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
                .trimmingCharacters(in: .whitespacesAndNewlines)
            if text.contains("-128") || text.localizedCaseInsensitiveContains("cancel") { throw Failure.cancelled }
            throw Failure.failed(String(text.suffix(300)))
        }
        for _ in 0..<30 where !isRunning { Thread.sleep(forTimeInterval: 0.5) }
        guard isRunning else { throw Failure.didNotStart }
    }
}
