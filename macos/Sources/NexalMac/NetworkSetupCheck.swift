import Foundation
import SwiftUI
import AppKit

/// Diagnosis for "VM has no address": the macOS Application Firewall or Little Snitch
/// blocking `bootpd` (the DHCP server the VM bridge relies on). Guidance only; nothing
/// here changes firewall settings or needs sudo.
enum NetworkSetupCheck {
    static let bootpdPath = "/usr/libexec/bootpd"

    enum FirewallState: Equatable { case enabled, disabled, unknown }
    enum BootpdRule: Equatable { case allowed, blocked, notListed }

    struct Result: Equatable {
        var firewall: FirewallState
        var bootpd: BootpdRule
        var littleSnitchRunning: Bool

        /// True when the Application Firewall is on and bootpd is not explicitly allowed.
        var firewallBlocksBootpd: Bool {
            firewall == .enabled && bootpd != .allowed
        }
        var hasProblem: Bool { firewallBlocksBootpd || littleSnitchRunning }

        /// Plain-language findings with the exact fix. One entry per problem.
        var messages: [String] {
            var out: [String] = []
            if firewallBlocksBootpd {
                let why = bootpd == .blocked
                    ? "The macOS Firewall is set to block \(NetworkSetupCheck.bootpdPath)"
                    : "The macOS Firewall is on and \(NetworkSetupCheck.bootpdPath) is not on its allowed list"
                out.append("\(why). Virtual machines can then not get an address. Fix: open System Settings > Network > Firewall > Options, click +, press Cmd-Shift-G, enter \(NetworkSetupCheck.bootpdPath), add it and set it to \"Allow incoming connections\".")
            }
            if littleSnitchRunning {
                out.append("Little Snitch is running and may be blocking bootpd. Fix: open Little Snitch, add a rule that allows the process \(NetworkSetupCheck.bootpdPath) (incoming and outgoing, any server, UDP ports 67 and 68), then restart the virtual machine.")
            }
            if out.isEmpty {
                out.append("Neither the macOS Firewall nor Little Snitch appears to block bootpd. If the VM still has no address, reinstall the home-network bridge and restart the VM.")
            }
            return out
        }
    }

    /// Parses `socketfilterfw --getglobalstate`, e.g. "Firewall is enabled. (State = 1)".
    static func parseGlobalState(_ output: String) -> FirewallState {
        let text = output.lowercased()
        if text.contains("state = 0") || text.contains("is disabled") { return .disabled }
        if text.contains("state = 1") || text.contains("state = 2") || text.contains("is enabled") { return .enabled }
        return .unknown
    }

    /// Parses `socketfilterfw --listapps`: a path line ("1 :  /usr/libexec/bootpd")
    /// followed by "( Allow incoming connections )" or "( Block incoming connections )".
    static func parseBootpdRule(_ output: String) -> BootpdRule {
        let lines = output.components(separatedBy: "\n")
        for (i, line) in lines.enumerated() where line.contains(bootpdPath) {
            let rest = lines.dropFirst(i + 1)
            for next in rest.prefix(2) {
                let lower = next.lowercased()
                if lower.contains("block") { return .blocked }
                if lower.contains("allow") { return .allowed }
            }
            return .notListed
        }
        return .notListed
    }

    /// Parses `ps -axo comm=` (full executable paths) for the Little Snitch extension/app.
    static func parseLittleSnitchRunning(_ processList: String) -> Bool {
        let text = processList.lowercased()
        return text.contains("at.obdev.littlesnitch") || text.contains("littlesnitch")
    }

    static func evaluate(globalState: String, listApps: String, processList: String) -> Result {
        Result(firewall: parseGlobalState(globalState),
               bootpd: parseBootpdRule(listApps),
               littleSnitchRunning: parseLittleSnitchRunning(processList))
    }

    /// Runs the read-only commands (no sudo). Call off the main thread.
    static func run() -> Result {
        let fw = "/usr/libexec/ApplicationFirewall/socketfilterfw"
        return evaluate(globalState: capture(fw, ["--getglobalstate"]),
                        listApps: capture(fw, ["--listapps"]),
                        processList: capture("/bin/ps", ["-axo", "comm="]))
    }

    private static func capture(_ path: String, _ args: [String]) -> String {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: path)
        p.arguments = args
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = Pipe()
        do { try p.run() } catch { return "" }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return String(decoding: data, as: UTF8.self)
    }

    static let firewallSettingsURL = URL(string: "x-apple.systempreferences:com.apple.preference.security?Firewall")!
}

/// Self-check row. Runs on demand, and automatically once when `autoRun` turns true
/// (bridge set up but a running VM has no LAN address).
struct NetworkSetupCheckView: View {
    var autoRun: Bool
    @State private var result: NetworkSetupCheck.Result?
    @State private var checking = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Button("Check VM networking") { runCheck() }.controlSize(.small).disabled(checking)
                if checking { ProgressView().controlSize(.small) }
            }
            if let result {
                ForEach(Array(result.messages.enumerated()), id: \.offset) { _, message in
                    Text(message).font(.caption)
                        .foregroundStyle(result.hasProblem ? Color.red : Color.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                HStack {
                    if result.firewallBlocksBootpd {
                        Button("Open Firewall settings") { NSWorkspace.shared.open(NetworkSetupCheck.firewallSettingsURL) }
                            .controlSize(.small)
                    }
                    if result.littleSnitchRunning {
                        Button("Open Little Snitch") {
                            if let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "at.obdev.littlesnitch") {
                                NSWorkspace.shared.open(url)
                            }
                        }.controlSize(.small)
                    }
                }
            }
        }
        .onChange(of: autoRun) { newValue in
            if newValue && result == nil { runCheck() }
        }
        .onAppear { if autoRun && result == nil { runCheck() } }
    }

    private func runCheck() {
        guard !checking else { return }
        checking = true
        DispatchQueue.global(qos: .userInitiated).async {
            let r = NetworkSetupCheck.run()
            DispatchQueue.main.async { result = r; checking = false }
        }
    }
}
