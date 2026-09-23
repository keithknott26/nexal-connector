import Foundation

struct SMBPresentation: Equatable {
    enum State: Equatable { case notAuthorized, needsMacSharing, available }
    let state: State
    let finderAddress: String?
    let detail: String

    static func derive(_ report: ConnectorStatus.MeshFileSharing?) -> SMBPresentation {
        guard let report, report.authorized else {
            return SMBPresentation(state: .notAuthorized, finderAddress: nil,
                                   detail: "File Sharing is not authorized for this computer. Access remains denied by default.")
        }
        guard report.available else {
            return SMBPresentation(state: .needsMacSharing, finderAddress: nil,
                                   detail: report.detail ?? "Turn on File Sharing in System Settings, then return here. neXal will not change that setting or your firewall.")
        }
        guard let address = safeAddress(host: report.address, share: report.shareName) else {
            return SMBPresentation(state: .needsMacSharing, finderAddress: nil,
                                   detail: "The networking service did not provide a safe Finder address.")
        }
        return SMBPresentation(state: .available, finderAddress: address,
                               detail: report.detail ?? "Available to authorized computers on your neXal network.")
    }

    private static func safeAddress(host: String?, share: String?) -> String? {
        guard let host, isNexalHostname(host),
              !host.contains("/"), !host.contains("@"), !host.contains("\\"),
              host.unicodeScalars.allSatisfy({ !$0.properties.isWhitespace }) else { return nil }
        var components = URLComponents()
        components.scheme = "smb"
        components.host = host
        if let share, !share.isEmpty { components.path = "/" + share }
        guard let value = components.url?.absoluteString, value.hasPrefix("smb://") else { return nil }
        return value
    }

    static func isNexalHostname(_ host: String) -> Bool {
        host.count <= 253 && host.hasSuffix(".mesh.nexal.systems") &&
        !host.contains("/") && !host.contains("@") && !host.contains("\\") &&
        host.unicodeScalars.allSatisfy({ !$0.properties.isWhitespace })
    }
}

struct ScreenSharingPresentation: Equatable {
    enum State: Equatable { case notAuthorized, needsMacSharing, available }
    let state: State; let address: String?; let detail: String
    static func derive(_ report: ConnectorStatus.MeshScreenSharing?) -> ScreenSharingPresentation {
        guard let report, report.authorized else { return .init(state: .notAuthorized, address: nil, detail: "Screen Sharing is not authorized. TCP 5900 remains denied by default.") }
        guard report.available else { return .init(state: .needsMacSharing, address: nil, detail: report.detail ?? "Turn on Screen Sharing in System Settings. neXal will not enable it or Remote Management.") }
        guard let host = report.address, SMBPresentation.isNexalHostname(host) else { return .init(state: .needsMacSharing, address: nil, detail: "A private neXal hostname is not ready.") }
        return .init(state: .available, address: "vnc://\(host)", detail: report.detail ?? "Available to authorized computers on your neXal network.")
    }
}

struct DiscoveryPresentation: Equatable {
    let title: String
    let detail: String

    static func derive(_ report: ConnectorStatus.MeshDiscovery?) -> DiscoveryPresentation {
        guard let report else { return .init(title: "Discovery unavailable", detail: "No discovery service has reported yet.") }
        if report.bridge == "active" {
            return .init(title: "Discovery Bridge active", detail: report.detail ?? "Authenticated SMB service records are available across authorized sites.")
        }
        if report.gateway == "active" {
            return .init(title: "Site gateway active", detail: report.detail ?? "Local SMB advertisements are being republished as sanitized, short-lived records.")
        }
        if report.wideAreaBonjour {
            return .init(title: "Wide-Area Bonjour active", detail: report.detail ?? "SMB services are discoverable through the configured DNS browse domain.")
        }
        return .init(title: "Discovery unavailable", detail: report.detail ?? "No gateway or authenticated discovery bridge is active. Local multicast is not routed between sites.")
    }
}
