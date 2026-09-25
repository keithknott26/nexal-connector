import Foundation

/// Product switches supplied by the coordinator in `status.features`.
///
/// Every remotely sensitive feature fails closed. A missing flag from an older
/// coordinator never makes SSH, screen sharing, chat, or calling appear. The
/// connector only presents a service after both the product flag and the
/// runtime's private-tunnel probe agree that it is available.
struct ConnectorFeatures: Decodable, Equatable {
    var remoteSSH = false
    var remoteVNC = false
    var networkFiles = false
    var wakeOnLAN = false
    var chat = false
    var videoConferencing = false

    private enum CodingKeys: String, CodingKey {
        case remoteSSH = "remote_ssh"
        case remoteVNC = "remote_vnc"
        case networkFiles = "network_files"
        case wakeOnLAN = "wake_on_lan"
        case chat
        case videoConferencing = "video_conferencing"
    }

    init() {}

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        remoteSSH = try c.decodeIfPresent(Bool.self, forKey: .remoteSSH) ?? false
        remoteVNC = try c.decodeIfPresent(Bool.self, forKey: .remoteVNC) ?? false
        networkFiles = try c.decodeIfPresent(Bool.self, forKey: .networkFiles) ?? false
        wakeOnLAN = try c.decodeIfPresent(Bool.self, forKey: .wakeOnLAN) ?? false
        chat = try c.decodeIfPresent(Bool.self, forKey: .chat) ?? false
        videoConferencing = try c.decodeIfPresent(Bool.self, forKey: .videoConferencing) ?? false
    }
}
