import Foundation

/// ~/Library/Logs/Nexal/agent.log: the agent's stderr plus the app's own
/// start/stop notes. Trimmed to the last 512 KiB when it grows past 2 MiB.
enum AgentLog {
    static var url: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Logs/Nexal/agent.log")
    }

    /// Append-mode handle for a child process's stderr.
    static func handle() -> FileHandle? {
        prepare()
        guard let h = try? FileHandle(forWritingTo: url) else { return nil }
        _ = try? h.seekToEnd()
        return h
    }

    static func note(_ text: String) {
        prepare()
        let line = "{\"time\":\"\(ISO8601DateFormatter().string(from: Date()))\",\"app\":\"\(text.replacingOccurrences(of: "\"", with: "'"))\"}\n"
        guard let h = try? FileHandle(forWritingTo: url) else { return }
        defer { try? h.close() }
        _ = try? h.seekToEnd()
        try? h.write(contentsOf: Data(line.utf8))
    }

    private static func prepare() {
        let fm = FileManager.default
        try? fm.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        if !fm.fileExists(atPath: url.path) {
            fm.createFile(atPath: url.path, contents: nil, attributes: [.posixPermissions: 0o600])
            return
        }
        if let size = (try? fm.attributesOfItem(atPath: url.path))?[.size] as? NSNumber,
           size.intValue > 2 << 20, let data = try? Data(contentsOf: url) {
            try? data.suffix(512 << 10).write(to: url, options: .atomic)
        }
    }
}
