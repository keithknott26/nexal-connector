import Foundation
import Darwin

/// Which sharing services are listening on this Mac, checked by a short TCP connect
/// to the loopback address. Read-only; changes nothing.
enum LocalServiceProbe {
    struct Result: Equatable {
        var remoteLogin = false   // SSH, port 22
        var screenSharing = false // VNC, port 5900
        var fileSharing = false   // SMB, port 445
    }

    static func run() async -> Result {
        await Task.detached(priority: .utility) {
            Result(remoteLogin: listening(22), screenSharing: listening(5900), fileSharing: listening(445))
        }.value
    }

    private static func listening(_ port: UInt16) -> Bool {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { return false }
        defer { close(fd) }
        _ = fcntl(fd, F_SETFL, fcntl(fd, F_GETFL, 0) | O_NONBLOCK)
        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = port.bigEndian
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        let started = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        if started == 0 { return true }
        guard errno == EINPROGRESS else { return false }
        var poller = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
        guard poll(&poller, 1, 300) == 1 else { return false }
        var error: Int32 = 0
        var length = socklen_t(MemoryLayout<Int32>.size)
        guard getsockopt(fd, SOL_SOCKET, SO_ERROR, &error, &length) == 0 else { return false }
        return error == 0
    }
}
