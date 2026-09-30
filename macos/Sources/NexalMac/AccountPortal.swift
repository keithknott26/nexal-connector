import Foundation

/// Account credentials stay in the browser; this connector keeps its device credential.
/// All people can authorize this Mac through the same enrollment flow after choosing a workspace.
enum AccountPortal {
    static func url(coordinator: String) -> URL? {
        guard var parts = URLComponents(string: coordinator), parts.scheme == "https",
              let host = parts.host, !host.isEmpty, parts.user == nil, parts.password == nil,
              parts.query == nil, parts.fragment == nil, parts.path.isEmpty || parts.path == "/" else { return nil }
        parts.path = "/"; parts.fragment = "/account"
        return parts.url
    }
}
