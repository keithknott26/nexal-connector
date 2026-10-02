import Foundation

/// Build protocol links without interpreting a host or share as URL credentials,
/// query parameters, or a different scheme.
enum PeerServiceURL {
    static func make(scheme: String, host: String, user: String? = nil, share: String? = nil) -> URL? {
        guard ["ssh", "vnc", "smb"].contains(scheme), !host.isEmpty,
              host.rangeOfCharacter(from: .whitespacesAndNewlines) == nil,
              !host.contains("/"), !host.contains("@"), !host.contains("?"),
              !host.contains("#") else { return nil }
        var parts = URLComponents()
        parts.scheme = scheme
        // Username only: a password in a URL lands in history, logs and the clipboard.
        // Finder/Screen Sharing fill the password from Keychain.
        if let user, !user.isEmpty {
            var allowed = CharacterSet.urlUserAllowed
            allowed.remove(charactersIn: ":@/%?#")
            guard let encoded = user.addingPercentEncoding(withAllowedCharacters: allowed) else { return nil }
            parts.percentEncodedUser = encoded
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
}
