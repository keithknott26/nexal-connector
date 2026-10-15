import AppKit
import CryptoKit
import Foundation
import SwiftUI

// Self-update from GitHub Releases. The release workflow (release-dmg.yml) attaches a signed,
// notarized `Nexal-Connector-<version>-universal.dmg` and its `.sha256` to each `v*` tag.
// This checks the latest release periodically, tells the user, and, on their confirmation,
// downloads it, verifies checksum + signature, and swaps the app in after quitting.

/// Pure logic: versions, release parsing, asset choice, checksum parsing, install script.
enum AppUpdate {
    static let repository = "keithknott26/nexal-connector"
    static var latestReleaseURL: URL { URL(string: "https://api.github.com/repos/\(repository)/releases/latest")! }
    static let checkInterval: TimeInterval = 6 * 3600

    struct Release: Equatable {
        let version: String
        let notes: String
        let pageURL: URL?
        let dmgURL: URL
        let checksumURL: URL?
        let dmgName: String
    }

    /// Numeric components of "v1.2.3" / "1.2.3-rc1" ("rc1" ignored); nil when none parse.
    static func components(_ version: String) -> [Int]? {
        var v = version.trimmingCharacters(in: .whitespaces)
        if v.hasPrefix("v") || v.hasPrefix("V") { v.removeFirst() }
        let core = v.split(whereSeparator: { $0 == "-" || $0 == "+" }).first.map(String.init) ?? v
        let parts = core.split(separator: ".").map { Int($0) }
        guard !parts.isEmpty, !parts.contains(where: { $0 == nil }) else { return nil }
        return parts.compactMap { $0 }
    }

    /// True when `candidate` is strictly newer than `current`. Unparseable input is never newer.
    static func isNewer(_ candidate: String, than current: String) -> Bool {
        guard var a = components(candidate), var b = components(current) else { return false }
        while a.count < b.count { a.append(0) }
        while b.count < a.count { b.append(0) }
        return a.lexicographicallyPrecedes(b) == false && a != b
    }

    /// The newest installable release from a GitHub "latest release" payload, or nil for drafts,
    /// prereleases, or releases with no signed universal DMG (an UNSIGNED build is never offered).
    static func release(from data: Data) -> Release? {
        guard let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              object["draft"] as? Bool != true, object["prerelease"] as? Bool != true,
              let tag = object["tag_name"] as? String, components(tag) != nil,
              let assets = object["assets"] as? [[String: Any]] else { return nil }
        func url(_ asset: [String: Any]) -> URL? {
            (asset["browser_download_url"] as? String).flatMap(URL.init(string:)).flatMap { $0.scheme == "https" ? $0 : nil }
        }
        guard let dmg = assets.first(where: {
            guard let name = $0["name"] as? String else { return false }
            return name.hasPrefix("Nexal-Connector-") && name.hasSuffix("-universal.dmg") && !name.uppercased().contains("UNSIGNED")
        }), let dmgName = dmg["name"] as? String, let dmgURL = url(dmg) else { return nil }
        let sum = assets.first { ($0["name"] as? String) == dmgName + ".sha256" }.flatMap(url)
        var version = tag
        if version.hasPrefix("v") || version.hasPrefix("V") { version.removeFirst() }
        return Release(version: version, notes: (object["body"] as? String) ?? "",
                       pageURL: (object["html_url"] as? String).flatMap(URL.init(string:)),
                       dmgURL: dmgURL, checksumURL: sum, dmgName: dmgName)
    }

    /// The hex digest from a `shasum -a 256` line ("<hex>  <name>"), for the named file.
    static func expectedDigest(in text: String, for name: String) -> String? {
        for line in text.split(whereSeparator: \.isNewline) {
            let parts = line.split(whereSeparator: { $0 == " " || $0 == "\t" || $0 == "*" }).map(String.init)
            guard parts.count >= 1, parts[0].count == 64, parts[0].allSatisfy(\.isHexDigit) else { continue }
            if parts.count == 1 || parts[1] == name { return parts[0].lowercased() }
        }
        return nil
    }

    static func sha256(of file: URL) throws -> String {
        let handle = try FileHandle(forReadingFrom: file)
        defer { try? handle.close() }
        var hasher = SHA256()
        while let chunk = try handle.read(upToCount: 1 << 20), !chunk.isEmpty { hasher.update(data: chunk) }
        return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    }

    static func shellQuote(_ s: String) -> String { "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'" }

    /// Swaps `staged` in for `destination`, keeping the old copy until the new one is in place and
    /// restoring it on failure. Runs as root when `privileged`, else as the user.
    static func swapCommand(staged: String, destination: String) -> String {
        let s = shellQuote(staged), d = shellQuote(destination), old = shellQuote(destination + ".old")
        return "rm -rf \(old); mv \(d) \(old) && /usr/bin/ditto \(s) \(d) && rm -rf \(old) || { rm -rf \(d); mv \(old) \(d); exit 1; }"
    }

    /// The detached helper: waits for the app to quit, stops its connector, swaps the bundle (asking
    /// for an administrator password only when the folder is not writable), and relaunches.
    static func installScript(pid: Int32, staged: String, destination: String, stagingDir: String) -> String {
        let swap = shellQuote(swapCommand(staged: staged, destination: destination))
        let dest = shellQuote(destination)
        // The privileged branch hands the command to AppleScript as an argument, so no quoting
        // layer has to be escaped twice.
        return """
        #!/bin/bash
        CMD=\(swap)
        while kill -0 \(pid) 2>/dev/null; do sleep 0.5; done
        pkill -f \(shellQuote(destination + "/Contents/Helpers/nexal run")) 2>/dev/null
        sleep 1
        if [ -w "$(dirname \(dest))" ] && [ -w \(dest) ]; then
          /bin/bash -c "$CMD"
        else
          /usr/bin/osascript -e 'on run argv' -e 'do shell script (item 1 of argv) with administrator privileges' -e 'end run' "$CMD"
        fi
        rm -rf \(shellQuote(stagingDir))
        /usr/bin/open \(dest)
        """
    }
}

@MainActor
final class AppUpdater: ObservableObject {
    static let shared = AppUpdater()

    enum Phase: Equatable {
        case idle, checking
        case available(AppUpdate.Release)
        case downloading
        case verifying, installing
        case failed(String)
    }

    @Published private(set) var phase: Phase = .idle
    @Published private(set) var lastChecked: Date?
    /// "Up to date" / error text from the last manual check, shown in Settings.
    @Published private(set) var checkMessage: String?

    private let defaults = UserDefaults.standard
    private var loop: Task<Void, Never>?
    private var installTask: Task<Void, Never>?
    private var promptedThisLaunch = false

    var automaticChecks: Bool {
        get { defaults.object(forKey: "nexal.updates.auto") as? Bool ?? true }
        set { defaults.set(newValue, forKey: "nexal.updates.auto"); objectWillChange.send(); newValue ? start() : stop() }
    }
    private var skippedVersion: String? {
        get { defaults.string(forKey: "nexal.updates.skipped") }
        set { defaults.set(newValue, forKey: "nexal.updates.skipped") }
    }
    var currentVersion: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0"
    }

    /// Starts the periodic check: shortly after launch, then every six hours.
    func start() {
        guard automaticChecks, loop == nil else { return }
        loop = Task { [weak self] in
            try? await Task.sleep(for: .seconds(30))
            while !Task.isCancelled {
                guard let self else { return }
                let due = (self.defaults.object(forKey: "nexal.updates.lastCheck") as? Date)
                    .map { Date().timeIntervalSince($0) >= AppUpdate.checkInterval } ?? true
                if due { await self.check(manual: false) }
                try? await Task.sleep(for: .seconds(3600))
            }
        }
    }
    func stop() { loop?.cancel(); loop = nil }

    func check(manual: Bool) async {
        switch phase { case .checking, .downloading, .verifying, .installing: return; default: break }
        phase = .checking
        var request = URLRequest(url: AppUpdate.latestReleaseURL, timeoutInterval: 20)
        request.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        request.setValue("neXal-home/\(currentVersion)", forHTTPHeaderField: "User-Agent")
        do {
            let (data, response) = try await URLSession.shared.data(for: request)
            let code = (response as? HTTPURLResponse)?.statusCode ?? 0
            defaults.set(Date(), forKey: "nexal.updates.lastCheck"); lastChecked = Date()
            guard code == 200 else {
                phase = .idle
                if manual { checkMessage = code == 404 ? "No releases are published yet." : "The update server answered \(code)." }
                return
            }
            if let release = AppUpdate.release(from: data), AppUpdate.isNewer(release.version, than: currentVersion) {
                if !manual, release.version == skippedVersion { phase = .idle; return }
                phase = .available(release); checkMessage = nil
                if !promptedThisLaunch || manual { promptedThisLaunch = true; prompt(for: release) }
            } else {
                phase = .idle
                if manual { checkMessage = "neXal@home \(currentVersion) is up to date." }
            }
        } catch {
            phase = .idle
            if manual { checkMessage = "Couldn't check for updates: \(error.localizedDescription)" }
        }
    }

    private func prompt(for release: AppUpdate.Release) {
        let alert = NSAlert()
        alert.messageText = "neXal@home \(release.version) is available"
        alert.informativeText = "You have \(currentVersion). Download and install it now? neXal@home will quit and reopen."
        alert.addButton(withTitle: "Install and Relaunch")
        alert.addButton(withTitle: "Later")
        alert.addButton(withTitle: "Skip This Version")
        NSApp.activate(ignoringOtherApps: true)
        switch alert.runModal() {
        case .alertFirstButtonReturn: install(release)
        case .alertThirdButtonReturn: skippedVersion = release.version; phase = .idle
        default: break
        }
    }

    func install(_ release: AppUpdate.Release) {
        guard installTask == nil else { return }
        installTask = Task { [weak self] in
            await self?.performInstall(release)
            self?.installTask = nil
        }
    }

    func skip(_ release: AppUpdate.Release) { skippedVersion = release.version; phase = .idle }

    private func performInstall(_ release: AppUpdate.Release) async {
        let fm = FileManager.default
        let staging = fm.temporaryDirectory.appendingPathComponent("nexal-update-\(UUID().uuidString)")
        var mount: URL?
        func fail(_ message: String) {
            if let mount { _ = Self.run("/usr/bin/hdiutil", ["detach", mount.path, "-force"]) }
            try? fm.removeItem(at: staging)
            phase = .failed(message)
        }
        do {
            try fm.createDirectory(at: staging, withIntermediateDirectories: true)
            phase = .downloading
            let dmg = staging.appendingPathComponent(release.dmgName)
            try await download(release.dmgURL, to: dmg)
            phase = .verifying
            // The checksum must be published with the release; without it the download is refused.
            guard let sumURL = release.checksumURL else { return fail("This release has no checksum file, so it was not installed.") }
            let (sumData, _) = try await URLSession.shared.data(from: sumURL)
            guard let expected = AppUpdate.expectedDigest(in: String(decoding: sumData, as: UTF8.self), for: release.dmgName) else {
                return fail("The release checksum file is unreadable.")
            }
            let actual = try await Task.detached { try AppUpdate.sha256(of: dmg) }.value
            guard actual == expected else { return fail("The download failed its checksum check, so it was not installed.") }

            let mountPoint = staging.appendingPathComponent("mnt")
            try fm.createDirectory(at: mountPoint, withIntermediateDirectories: true)
            let attach = Self.run("/usr/bin/hdiutil", ["attach", "-nobrowse", "-readonly", "-noverify", "-mountpoint", mountPoint.path, dmg.path])
            guard attach.status == 0 else { return fail("Couldn't open the downloaded disk image.") }
            mount = mountPoint
            let source = mountPoint.appendingPathComponent("neXal-Connector.app")
            guard fm.fileExists(atPath: source.path) else { return fail("The disk image doesn't contain neXal-Connector.app.") }
            let staged = staging.appendingPathComponent("neXal-Connector.app")
            guard Self.run("/usr/bin/ditto", [source.path, staged.path]).status == 0 else { return fail("Couldn't copy the new version.") }
            _ = Self.run("/usr/bin/hdiutil", ["detach", mountPoint.path, "-force"]); mount = nil

            guard Self.run("/usr/bin/codesign", ["--verify", "--deep", "--strict", staged.path]).status == 0,
                  Self.run("/usr/sbin/spctl", ["--assess", "--type", "execute", staged.path]).status == 0 else {
                return fail("The new version failed its signature check, so it was not installed.")
            }
            // Same publisher as the running app (skipped when the running copy is ad hoc signed).
            if let mine = Self.teamID(Bundle.main.bundleURL.path), let theirs = Self.teamID(staged.path), mine != theirs {
                return fail("The new version is signed by a different developer, so it was not installed.")
            }

            phase = .installing
            let destination = Bundle.main.bundleURL.path
            let script = staging.appendingPathComponent("install.sh")
            try AppUpdate.installScript(pid: ProcessInfo.processInfo.processIdentifier, staged: staged.path,
                                        destination: destination, stagingDir: staging.path)
                .write(to: script, atomically: true, encoding: .utf8)
            let helper = Process()
            helper.executableURL = URL(fileURLWithPath: "/bin/bash")
            helper.arguments = [script.path]
            helper.standardOutput = FileHandle.nullDevice; helper.standardError = FileHandle.nullDevice
            try helper.run()
            NSApp.terminate(nil)
        } catch {
            fail("Update failed: \(error.localizedDescription)")
        }
    }

    private func download(_ url: URL, to destination: URL) async throws {
        let (temp, response) = try await URLSession.shared.download(from: url)
        guard (response as? HTTPURLResponse)?.statusCode == 200 else { throw URLError(.badServerResponse) }
        try? FileManager.default.removeItem(at: destination)
        try FileManager.default.moveItem(at: temp, to: destination)
    }

    nonisolated private static func run(_ path: String, _ args: [String]) -> (status: Int32, output: String) {
        let p = Process(); p.executableURL = URL(fileURLWithPath: path); p.arguments = args
        let pipe = Pipe(); p.standardOutput = pipe; p.standardError = pipe
        do { try p.run() } catch { return (-1, "") }
        let data = pipe.fileHandleForReading.readDataToEndOfFile(); p.waitUntilExit()
        return (p.terminationStatus, String(decoding: data, as: UTF8.self))
    }
    nonisolated private static func teamID(_ path: String) -> String? {
        let out = run("/usr/bin/codesign", ["-dv", path]).output
        let line = out.split(separator: "\n").first { $0.hasPrefix("TeamIdentifier=") }
        guard let value = line?.dropFirst("TeamIdentifier=".count), value != "not set" else { return nil }
        return String(value)
    }
}

/// Shown in the panel while an update is available or being installed.
struct UpdateBanner: View {
    @ObservedObject private var updater = AppUpdater.shared

    var body: some View {
        Group {
            switch updater.phase {
            case .available(let release):
                box {
                    Label("Update available: neXal@home \(release.version)", systemImage: "arrow.down.circle.fill")
                        .font(.subheadline.weight(.semibold)).foregroundStyle(.blue)
                    Text("You have \(updater.currentVersion). neXal@home will quit and reopen to finish.")
                        .font(.caption).fixedSize(horizontal: false, vertical: true)
                    HStack {
                        Button("Install and Relaunch") { updater.install(release) }.controlSize(.small)
                        if let page = release.pageURL { Button("Release Notes") { NSWorkspace.shared.open(page) }.controlSize(.small) }
                        Button("Skip") { updater.skip(release) }.controlSize(.small)
                    }
                }
            case .downloading:
                box { ProgressView("Downloading update…").font(.caption) }
            case .verifying:
                box { ProgressView("Verifying update…").font(.caption) }
            case .installing:
                box { ProgressView("Installing, neXal@home will reopen…").font(.caption) }
            case .failed(let message):
                box {
                    Label("Update didn't install", systemImage: "exclamationmark.triangle.fill")
                        .font(.subheadline.weight(.semibold)).foregroundStyle(.orange)
                    Text(message).font(.caption).fixedSize(horizontal: false, vertical: true).textSelection(.enabled)
                }
            default: EmptyView()
            }
        }
        .accessibilityIdentifier("app-update")
    }

    private func box<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 6) { content() }
            .padding(10)
            .background(RoundedRectangle(cornerRadius: 8).fill(Color.blue.opacity(0.12)))
    }
}

/// The Settings row: automatic checks, manual check, current status.
struct UpdateSettingsGroup: View {
    @ObservedObject private var updater = AppUpdater.shared
    var body: some View {
        GroupBox("Software update") {
            VStack(alignment: .leading, spacing: 10) {
                Toggle("Check for updates automatically", isOn: Binding(
                    get: { updater.automaticChecks }, set: { updater.automaticChecks = $0 }))
                Text("Checks every few hours and asks before downloading or installing anything.")
                    .font(.caption).foregroundStyle(.secondary)
                HStack {
                    Button("Check Now") { Task { await updater.check(manual: true) } }
                        .disabled(updater.phase == .checking)
                    Text("Version \(updater.currentVersion)").font(.caption).foregroundStyle(.secondary)
                    if let message = updater.checkMessage { Text(message).font(.caption).foregroundStyle(.secondary) }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading).padding(4)
        }
    }
}
