import AppKit
import Foundation

/// Build protocol links without interpreting a host or share as URL credentials,
/// query parameters, or a different scheme.
enum PeerServiceURL {
    static func make(scheme: String, host: String, user: String? = nil, password: String? = nil, share: String? = nil) -> URL? {
        guard ["ssh", "vnc", "smb"].contains(scheme), !host.isEmpty,
              host.rangeOfCharacter(from: .whitespacesAndNewlines) == nil,
              !host.contains("/"), !host.contains("@"), !host.contains("?"),
              !host.contains("#") else { return nil }
        var parts = URLComponents()
        parts.scheme = scheme
        // Credentials are optional and only embedded when known (from Keychain). Callers
        // must never log or display the resulting URL; use `redacted`.
        if let user, !user.isEmpty {
            var allowed = CharacterSet.urlUserAllowed
            allowed.remove(charactersIn: ":@/%?#")
            guard let encoded = user.addingPercentEncoding(withAllowedCharacters: allowed) else { return nil }
            parts.percentEncodedUser = encoded
            if let password, !password.isEmpty, let pw = password.addingPercentEncoding(withAllowedCharacters: allowed) {
                parts.percentEncodedPassword = pw
            }
        }
        parts.host = host.contains(":") && !host.hasPrefix("[") ? "[\(host)]" : host
        if let share, !share.isEmpty {
            var allowed = CharacterSet.urlPathAllowed
            allowed.remove(charactersIn: "/%?#")
            guard let encoded = share.addingPercentEncoding(withAllowedCharacters: allowed) else { return nil }
            parts.percentEncodedPath = "/" + encoded
        }
        return parts.url
    }

    /// The same link with any password hidden, safe for tooltips and logs.
    static func redacted(_ url: URL) -> String {
        guard url.password != nil, var c = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return url.absoluteString }
        c.password = "••••"
        return c.string ?? url.host ?? ""
    }
}

import Security

/// Looks up an internet password the user already saved in Keychain for a peer
/// (Finder/Screen Sharing "Remember this password"). macOS may ask permission, so call
/// it only when the user clicks, never while drawing a view.
enum KeychainPassword {
    static func lookup(scheme: String, host: String) -> String? {
        guard scheme == "smb" || scheme == "vnc" else { return nil }
        var query: [String: Any] = [kSecClass as String: kSecClassInternetPassword,
                                    kSecAttrServer as String: host,
                                    kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
        query[kSecAttrProtocol as String] = scheme == "smb" ? kSecAttrProtocolSMB : "vnc " as CFString
        var out: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &out) == errSecSuccess, let data = out as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }
}

/// Launches ssh to a peer from a generated, self-deleting .command script so keepalive options apply
/// without touching the user's ssh config.
enum PeerSSHLauncher {
    static let keepAliveOptions = "-o ServerAliveInterval=15 -o ServerAliveCountMax=8 -o TCPKeepAlive=yes"

    /// POSIX single-quote quoting; nil for values that cannot be safely carried in a script.
    static func shellQuote(_ s: String) -> String? {
        guard !s.isEmpty, s.count <= 253, !s.unicodeScalars.contains(where: { $0.value < 0x20 || $0.value == 0x7f }) else { return nil }
        return "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    static func script(directory: String, user: String, host: String) -> String? {
        guard ThrowawayHosting.validHost(host), let u = shellQuote(user), let h = shellQuote(host),
              let d = shellQuote(directory), !user.hasPrefix("-") else { return nil }
        return """
        #!/bin/bash
        D=\(d)
        trap 'rm -rf "$D"' EXIT
        clear
        ssh \(keepAliveOptions) -l \(u) -- \(h)
        """
    }

    @MainActor
    static func open(user: String, host: String) -> Bool {
        let fm = FileManager.default
        let dir = fm.temporaryDirectory.appendingPathComponent("nexal-ssh-\(UUID().uuidString)", isDirectory: true)
        do {
            try fm.createDirectory(at: dir, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
            guard let text = script(directory: dir.path, user: user, host: host) else {
                try? fm.removeItem(at: dir); return false
            }
            let url = dir.appendingPathComponent("ssh.command")
            try text.write(to: url, atomically: true, encoding: .utf8)
            try fm.setAttributes([.posixPermissions: 0o700], ofItemAtPath: url.path)
            if NSWorkspace.shared.open(url) { return true }
            try? fm.removeItem(at: dir)
        } catch { try? fm.removeItem(at: dir) }
        return false
    }
}
