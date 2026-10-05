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
                return "The secure networking component is missing from this app. Reinstall neXal@home."
            case .helperUnsafe:
                return "The secure networking component in this app has been modified or can be changed by other users, so it was not run. Reinstall neXal@home."
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
                + "with prompt \"neXal needs to install its secure networking service, allow connections from your other computers in the macOS firewall (turning off Block all incoming connections and stealth mode), and turn on Wake for network access so other computers can wake this one.\" "
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
    /// "nx-exit-<32 hex>". Both are created by the coordinator with
    /// auto-apply off, so nothing is routed until this Mac selects one.
    static let storageExitRoute = "nexal-exit"

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
        guard (try? process.run()) != nil else { return (-1, "could not start the secure networking service") }
        let timeout = DispatchWorkItem { if process.isRunning { process.terminate() } }
        DispatchQueue.global().asyncAfter(deadline: .now() + 8, execute: timeout)
        defer { timeout.cancel() }
        var data = Data(), oversized = false
        while true {
            let chunk = out.fileHandleForReading.availableData
            if chunk.isEmpty { break }
            if data.count + chunk.count <= 65_536 { data.append(chunk) }
            else { oversized = true; if process.isRunning { process.terminate() } }
        }
        process.waitUntilExit()
        if oversized { return (-1, "The network service returned too much routing data.") }
        return (process.terminationStatus, String(decoding: data, as: UTF8.self))
    }

    enum RoutingFailure: LocalizedError {
        case failed(String)
        var errorDescription: String? { if case let .failed(message) = self { return message }; return nil }
    }

    struct ExitRoutes: Equatable {
        var available = Set<String>()
        var selected = Set<String>()
    }

    static func validExitRouteID(_ value: String) -> Bool {
        value.range(of: #"^(?:nexal-exit(?:-[a-z0-9-]+)?|nx-exit-[a-f0-9]{32})$"#, options: .regularExpression) != nil
    }

    /// Read IDs and selection from complete default-route records, never from
    /// arbitrary descriptions or a saved preference. Subnet routes are excluded.
    static func parseExitRoutes(_ text: String) -> ExitRoutes {
        var result = ExitRoutes(), id: String?, isDefault = false, selected = false
        func finish() {
            guard let id, validExitRouteID(id), isDefault else { return }
            result.available.insert(id)
            if selected { result.selected.insert(id) }
        }
        for raw in text.components(separatedBy: .newlines) {
            let line = raw.trimmingCharacters(in: .whitespaces)
            if line.hasPrefix("- ID: ") {
                finish(); id = String(line.dropFirst(6)); isDefault = false; selected = false
            } else if line.hasPrefix("Network: ") {
                let networks = line.dropFirst(9).split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }
                isDefault = networks.contains("0.0.0.0/0") || networks.contains("::/0")
            } else if line == "Status: Selected" { selected = true }
        }
        finish()
        return result
    }

    static func exitRoutes() -> ExitRoutes? {
        let (code, text) = runHelper(["networks", "ls"])
        return code == 0 ? parseExitRoutes(text) : nil
    }

    static func availableExitRoutes() -> Set<String> { exitRoutes()?.available ?? [] }

    /// Change only the requested exit route, preserving unrelated subnet routes.
    /// Verify the runtime receipt and restore the previous selection on failure.
    static func selectExitRoute(_ id: String?, previous: String?,
                                execute: ([String]) -> (Int32, String) = runHelper) throws {
        guard [id, previous].compactMap({ $0 }).allSatisfy(validExitRouteID) else {
            throw RoutingFailure.failed("This exit route is not valid.")
        }
        if let previous, previous != id {
            let (code, text) = execute(["networks", "deselect", previous])
            if code != 0 { throw RoutingFailure.failed(text.trimmingCharacters(in: .whitespacesAndNewlines)) }
        }
        do {
            if let id {
                let (code, text) = execute(["networks", "select", "--append", id])
                if code != 0 { throw RoutingFailure.failed(text.trimmingCharacters(in: .whitespacesAndNewlines)) }
            }
            let (code, text) = execute(["networks", "ls"])
            let state = parseExitRoutes(text)
            guard code == 0, id.map({ state.selected.contains($0) }) ?? (previous.map({ !state.selected.contains($0) }) ?? true) else {
                throw RoutingFailure.failed("The network service has not confirmed the requested exit route.")
            }
        } catch {
            if let id { _ = execute(["networks", "deselect", id]) }
            if let previous { _ = execute(["networks", "select", "--append", previous]) }
            throw error
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
        let what = [firewall ? "allow connections from your other computers in the macOS firewall (turning off Block all incoming connections and stealth mode)" : nil,
                    lazy ? "keep connections to your other computers always on" : nil,
                    wake ? "turn on Wake for network access so other computers can wake this one" : nil]
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

    /// Install a root launchd deadline guard before a guest tunnel can start.
    /// It survives quitting this app and needs no coordinator polling.
    static func installGuestExpiryGuard(config: URL) throws {
        let helper = try validatedHelper(Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/nexal"))
        let script = [
            "on run argv",
            "set p to quoted form of (item 1 of argv)",
            "set c to quoted form of (item 2 of argv)",
            "do shell script p & \" guest install-guard --config \" & c with prompt \"neXal needs an automatic expiry guard to disconnect temporary network access after one hour, even when this app is closed or the internet is unavailable.\" with administrator privileges",
            "end run",
        ]
        try runAdminScript(script, argument: helper.path, additionalArguments: [config.path])
    }

    static var guestExpiryGuardInstalled: Bool {
        FileManager.default.fileExists(atPath: "/Library/LaunchDaemons/systems.nexal.guest-expiry.plist")
    }

    static func removeGuestExpiryGuard() throws {
        let helper = try validatedHelper(Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/nexal"))
        let script = [
            "on run argv",
            "set p to quoted form of (item 1 of argv)",
            "do shell script p & \" guest remove-guard\" with prompt \"neXal needs to disconnect the previous temporary access before pairing this Mac as one of your own computers.\" with administrator privileges",
            "end run",
        ]
        try runAdminScript(script, argument: helper.path)
    }

    private static func runAdminScript(_ script: [String], argument: String, additionalArguments: [String] = []) throws {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        process.arguments = script.flatMap { ["-e", $0] } + [argument] + additionalArguments
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
