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
            "set fw to \"/usr/libexec/ApplicationFirewall/socketfilterfw\"",
            "set firewallCommands to " + firewallCommandsExpression,
            // The runtime receives the encrypted tunnel on its own UDP socket, so
            // the macOS application firewall must allow it inbound. Left blocked,
            // peers can never reach this Mac directly and every connection falls
            // back to a relay (or fails). Allowing it is scoped to this binary.
            // "Block all incoming connections" overrides every per-app rule, and
            // stealth mode drops peer probes, so both are turned off here too.
            "do shell script p & \" service install >/dev/null 2>&1; \" "
                + "& firewallCommands "
                + "& p & \" service start\" "
                + "with prompt \"neXal needs to install its secure networking service and allow incoming peer connections in the macOS firewall (this turns off Block all incoming connections and stealth mode).\" "
                + "with administrator privileges",
            "end run",
        ]
        try runAdminScript(script, argument: helper.path)
        for _ in 0..<30 where !isRunning { Thread.sleep(forTimeInterval: 0.5) }
        guard isRunning else { throw Failure.didNotStart }
    }

    // MARK: - macOS application firewall

    static let firewallTool = "/usr/libexec/ApplicationFirewall/socketfilterfw"

    /// AppleScript expression (needs `fw` and `p` in scope) for the root shell
    /// commands that let peers reach this Mac directly: turn off "Block all
    /// incoming connections" (it overrides every per-app rule), turn off stealth
    /// mode, and allow the bundled runtime inbound. Each ends in "; " so it can
    /// be prefixed to another command.
    static let firewallCommandsExpression =
        "fw & \" --setblockall off >/dev/null 2>&1; \" "
        + "& fw & \" --setstealthmode off >/dev/null 2>&1; \" "
        + "& fw & \" --add \" & p & \" >/dev/null 2>&1; \" "
        + "& fw & \" --unblockapp \" & p & \" >/dev/null 2>&1; \""

    /// Reads a firewall setting. Getters do not need root.
    private static func firewallSetting(_ flag: String) -> String {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: firewallTool)
        process.arguments = [flag]
        let out = Pipe()
        process.standardOutput = out
        process.standardError = FileHandle.nullDevice
        guard (try? process.run()) != nil else { return "" }
        process.waitUntilExit()
        return String(decoding: out.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self).lowercased()
    }

    /// Output wording differs across macOS versions ("enabled"/"disabled",
    /// "is on"/"is off"); "disabled" does not contain "enabled".
    private static func isOn(_ text: String) -> Bool {
        text.contains("enabled") || text.contains(" is on") || text.contains(" on.")
    }

    /// True when "Block all incoming connections" or stealth mode is on, which
    /// keeps peers from connecting to this Mac directly.
    static var firewallBlocksPeers: Bool {
        isOn(firewallSetting("--getblockall")) || isOn(firewallSetting("--getstealthmode"))
    }

    /// Blocking: one administrator prompt that turns off block-all and stealth
    /// mode and re-allows the bundled runtime. For Macs whose service is already
    /// installed (so `install()` never runs again). Call off the main actor.
    static func repairFirewall() throws {
        let helper = try validatedHelper()
        let script = [
            "on run argv",
            "set p to quoted form of (item 1 of argv)",
            "set fw to \"\(firewallTool)\"",
            "do shell script " + firewallCommandsExpression + " & \"true\" "
                + "with prompt \"neXal needs to allow incoming peer connections in the macOS firewall. This turns off Block all incoming connections and stealth mode.\" "
                + "with administrator privileges",
            "end run",
        ]
        try runAdminScript(script, argument: helper.path)
        guard !firewallBlocksPeers else {
            throw Failure.failed("macOS still reports Block all incoming connections or stealth mode as on. A configuration profile (MDM) may be enforcing it.")
        }
    }

    private static func runAdminScript(_ script: [String], argument: String) throws {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        process.arguments = script.flatMap { ["-e", $0] } + [argument]
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
    }
}
