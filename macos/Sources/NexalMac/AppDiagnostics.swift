import Foundation
import os

/// The app's own diagnostic events: panel opened, peer expanded, service link
/// opened, wake result, errors shown.
///
/// Every event is mirrored to the unified log (subsystem `systems.nexal.home`,
/// visible in Console.app). While diagnostic mode is on, it is also appended as
/// one JSON line to ~/Library/Logs/Nexal/app.log (capped at 5 MiB, one
/// rotation), next to the agent's agent.log and diagnostics.log.
///
/// Callers pass values that are already safe to keep: a service link goes
/// through `PeerServiceURL.redacted`, never the raw URL, and no password,
/// token, setup key or Keychain item is ever a field.
enum AppDiagnostics {
    static let subsystem = "systems.nexal.home"
    /// The agent's flag file name (connector/internal/diaglog.FlagName).
    static let flagName = "diagnostics.enabled"
    private static let maxBytes = 5 << 20
    private static let queue = DispatchQueue(label: "systems.nexal.home.app-diagnostics", qos: .utility)

    static var logDirectory: URL {
        FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Logs/Nexal", isDirectory: true)
    }
    static var appLogURL: URL { logDirectory.appendingPathComponent("app.log") }
    static var agentLogURL: URL { logDirectory.appendingPathComponent("agent.log") }
    static var diagnosticsLogURL: URL { logDirectory.appendingPathComponent("diagnostics.log") }

    /// Diagnostic mode is the agent's flag file next to config.json. Either
    /// profile's flag counts, so the app records events for whichever is in use.
    static var isEnabled: Bool {
        [ConnectorProcess.configURL, ConnectorProcess.developmentConfigURL].contains {
            FileManager.default.fileExists(atPath: $0.deletingLastPathComponent().appendingPathComponent(flagName).path)
        }
    }

    static func ui(_ message: String, _ fields: [String: String] = [:]) { event("ui", message, fields) }
    static func error(_ message: String, _ fields: [String: String] = [:]) { event("error", message, fields, level: "WARN") }

    static func event(_ category: String, _ message: String, _ fields: [String: String] = [:], level: String = "INFO") {
        let detail = fields.keys.sorted().map { "\($0)=\(fields[$0] ?? "")" }.joined(separator: " ")
        let text = detail.isEmpty ? message : "\(message) \(detail)"
        let logger = Logger(subsystem: subsystem, category: category)
        if level == "WARN" {
            logger.warning("\(text, privacy: .public)")
        } else {
            logger.info("\(text, privacy: .public)")
        }
        guard isEnabled else { return }
        var record: [String: String] = fields
        record["time"] = ISO8601DateFormatter().string(from: Date())
        record["level"] = level
        record["component"] = "app." + category
        record["msg"] = message
        guard var data = try? JSONSerialization.data(withJSONObject: record, options: [.sortedKeys]) else { return }
        data.append(0x0A)
        queue.async { append(data) }
    }

    private static func append(_ data: Data) {
        let fm = FileManager.default
        let url = appLogURL
        try? fm.createDirectory(at: logDirectory, withIntermediateDirectories: true)
        if let size = (try? fm.attributesOfItem(atPath: url.path))?[.size] as? NSNumber,
           size.intValue + data.count > maxBytes {
            let rotated = logDirectory.appendingPathComponent("app.log.1")
            try? fm.removeItem(at: rotated)
            try? fm.moveItem(at: url, to: rotated)
        }
        if !fm.fileExists(atPath: url.path) {
            fm.createFile(atPath: url.path, contents: nil, attributes: [.posixPermissions: 0o600])
        }
        guard let handle = try? FileHandle(forWritingTo: url) else { return }
        defer { try? handle.close() }
        _ = try? handle.seekToEnd()
        try? handle.write(contentsOf: data)
    }
}
