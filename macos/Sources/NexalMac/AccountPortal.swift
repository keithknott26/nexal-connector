import Foundation
import SwiftUI

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
struct AccountPortalEntry: View {
    @EnvironmentObject var model: AppModel
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("People & private networks").font(.headline)
            Text("Invite people, accept an invitation, or create your own network. Each person uses their own Apple account or configured organization SSO.").font(.caption).foregroundStyle(.secondary)
            if let url = AccountPortal.url(coordinator: model.coordinator) {
                Link("Open account & network sign-in", destination: url).buttonStyle(.bordered)
            }
            Text("In the browser, choose your workspace. To connect this Mac, show its one-time pairing code here and enter it under People & networks. Existing computers stay in their current network until you explicitly leave and pair again.").font(.caption).foregroundStyle(.secondary)
        }.padding(12).background(.secondary.opacity(0.07), in: RoundedRectangle(cornerRadius: 10))
    }
}
