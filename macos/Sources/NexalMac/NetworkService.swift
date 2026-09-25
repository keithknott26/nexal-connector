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

    /// The launchd definition `service install` writes.
    static let launchDaemonPlist = "/Library/LaunchDaemons/netbird.plist"

    /// The executable the registered service launches, if one is registered.
    static var registeredServiceBinary: String? {
        guard let data = FileManager.default.contents(atPath: launchDaemonPlist),
              let plist = try? PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any]
        else { return nil }
        if let args = plist["ProgramArguments"] as? [String], let first = args.first { return first }
        return plist["Program"] as? String
    }

    /// True when a service is registered but runs a binary other than this
    /// app's own helper, e.g. a copy left in the Trash after an update.
    static var serviceUsesOtherBinary: Bool {
        guard let registered = registeredServiceBinary else { return false }
        let mine = bundledHelper.resolvingSymlinksInPath().path
        return URL(fileURLWithPath: registered).resolvingSymlinksInPath().path != mine
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
            // Stop and unregister first: after the app is moved, updated or
            // reinstalled, a service registered from the OLD location keeps
            // running that stale binary (often from the Trash), which the
            // firewall then blocks. Re-registering pins it to this bundle.
            "do shell script p & \" service stop >/dev/null 2>&1; \" "
                + "& p & \" service uninstall >/dev/null 2>&1; \" "
                + "& p & \" service install --service-env NB_LAZY_CONN=off >/dev/null 2>&1; \" "
                + "& p & \" service reconfigure --service-env NB_LAZY_CONN=off >/dev/null 2>&1; \" "
                + "& \"\(wakeForNetworkCommand)\" "
                + "& firewallCommands "
                + "& p & \" service start\" "
                + "with prompt \"neXal needs to install its secure networking service, allow incoming peer connections in the macOS firewall (turning off Block all incoming connections and stealth mode), and turn on Wake for network access so other Macs can wake this one.\" "
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

    /// True when the running service reports lazy connections on. Lazy peers
    /// have no tunnel until traffic arrives, so the first request stalls. The
    /// account setting turns them on; NB_LAZY_CONN=off in the service
    /// environment overrides it on this Mac (the `up` flag is ignored since 0.75).
    // MARK: - Exit node

    /// Route id of the neXal Storage exit route; another peer's is
    /// "nexal-exit-<peer name>". Both are created by the coordinator with
    /// auto-apply off, so nothing is routed until this Mac selects one.
    static let storageExitRoute = "nexal-exit"
    static func exitRoute(forPeerNamed name: String) -> String { "nexal-exit-\(name.lowercased())" }

    /// Runs the bundled runtime unprivileged (it talks to the service over its
    /// socket, exactly like `status`). Returns (exit status, stdout+stderr).
    static func runHelper(_ arguments: [String]) -> (Int32, String) {
        guard isRunning, let helper = try? validatedHelper() else { return (-1, "secure networking service is not running") }
        let process = Process()
        process.executableURL = helper
        process.arguments = arguments
        let out = Pipe()
        process.standardOutput = out
        process.standardError = out
        guard (try? process.run()) != nil else { return (-1, "could not start the secure networking runtime") }
        let data = out.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        return (process.terminationStatus, String(decoding: data, as: UTF8.self))
    }

    /// Exit routes this Mac has been given (ids starting with "nexal-exit").
    static func availableExitRoutes() -> Set<String> {
        let (code, text) = runHelper(["networks", "ls"])
        guard code == 0 else { return [] }
        var ids = Set<String>()
        let pattern = try? NSRegularExpression(pattern: #"\bnexal-exit(?:-[a-z0-9-]+)?\b"#)
        let range = NSRange(text.startIndex..., in: text)
        pattern?.enumerateMatches(in: text, range: range) { match, _, _ in
            if let match, let r = Range(match.range, in: text) { ids.insert(String(text[r])) }
        }
        return ids
    }

    /// Selects one exit route (and deselects the previous), or deselects when nil.
    static func selectExitRoute(_ id: String?, previous: String?) throws {
        if let previous, previous != id {
            let (code, text) = runHelper(["networks", "deselect", previous])
            if code != 0 && id == nil { throw Failure.failed(text.trimmingCharacters(in: .whitespacesAndNewlines)) }
        }
        if let id {
            // --append keeps any other selected routes; a runtime without the flag
            // gets the plain form (which replaces the selection).
            var (code, text) = runHelper(["networks", "select", "--append", id])
            if code != 0, text.localizedCaseInsensitiveContains("unknown flag") {
                (code, text) = runHelper(["networks", "select", id])
            }
            if code != 0 { throw Failure.failed(text.trimmingCharacters(in: .whitespacesAndNewlines)) }
        }
    }

    static var lazyConnectionsOn: Bool {
        guard isRunning, let helper = try? validatedHelper() else { return false }
        let process = Process()
        process.executableURL = helper
        process.arguments = ["status"]
        let out = Pipe()
        process.standardOutput = out
        process.standardError = FileHandle.nullDevice
        guard (try? process.run()) != nil else { return false }
        process.waitUntilExit()
        let text = String(decoding: out.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
        return text.range(of: "Lazy connection: true", options: .caseInsensitive) != nil
    }

    /// `pmset -a womp 1` = System Settings > Energy > "Wake for network access",
    /// on every power source. Lets a magic packet relayed by another neXal Mac
    /// wake this one. Ends in "; " so it can be prefixed to another command.
    static let wakeForNetworkCommand = "/usr/bin/pmset -a womp 1 >/dev/null 2>&1; "

    /// True when this Mac supports Wake for network access and it is off for
    /// any power source. `pmset -g custom` needs no root; a Mac that does not
    /// list `womp` at all cannot be woken this way and is left alone.
    static var wakeForNetworkOff: Bool {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/pmset")
        process.arguments = ["-g", "custom"]
        let out = Pipe()
        process.standardOutput = out
        process.standardError = FileHandle.nullDevice
        guard (try? process.run()) != nil else { return false }
        process.waitUntilExit()
        let text = String(decoding: out.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
        let values = text.split(separator: "\n").compactMap { line -> String? in
            let fields = line.split(whereSeparator: { $0 == " " || $0 == "\t" })
            return fields.count >= 2 && fields[0] == "womp" ? String(fields[1]) : nil
        }
        return values.contains("0")
    }

    /// Blocking: one administrator prompt that fixes what `install()` sets on a
    /// Mac whose service was already installed (so `install()` never runs
    /// again): the firewall and/or lazy connections. Call off the main actor.
    static func repairHostSettings(firewall: Bool, lazy: Bool, wake: Bool = false) throws {
        guard firewall || lazy || wake else { return }
        let helper = try validatedHelper()
        var command = "\"\""
        if firewall { command += " & " + firewallCommandsExpression }
        if wake { command += " & \"\(wakeForNetworkCommand)\"" }
        if lazy {
            command += " & p & \" service reconfigure --service-env NB_LAZY_CONN=off >/dev/null 2>&1; \" "
                + "& p & \" service start >/dev/null 2>&1; \""
        }
        let what = [firewall ? "allow incoming peer connections in the macOS firewall (turning off Block all incoming connections and stealth mode)" : nil,
                    lazy ? "keep peer connections always on (turning off lazy connections)" : nil,
                    wake ? "turn on Wake for network access so other Macs can wake this one" : nil]
            .compactMap { $0 }.joined(separator: " and ")
        let script = [
            "on run argv",
            "set p to quoted form of (item 1 of argv)",
            "set fw to \"\(firewallTool)\"",
            "do shell script " + command + " & \"true\" "
                + "with prompt \"neXal needs to \(what).\" "
                + "with administrator privileges",
            "end run",
        ]
        try runAdminScript(script, argument: helper.path)
        if lazy { for _ in 0..<30 where !isRunning { Thread.sleep(forTimeInterval: 0.5) } }
        if wake && wakeForNetworkOff {
            throw Failure.failed("macOS still reports Wake for network access as off. A configuration profile (MDM) may be enforcing it.")
        }
        if firewall && firewallBlocksPeers {
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
