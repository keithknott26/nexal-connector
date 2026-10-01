import Foundation
import Darwin

/// Minimal unix-domain socket helpers shared by the control server/client and
/// the guest bridge.
enum UnixSocket {
    private static func address(_ path: String) throws -> sockaddr_un {
        var addr = sockaddr_un()
        addr.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8CString)
        let cap = MemoryLayout.size(ofValue: addr.sun_path)
        guard bytes.count <= cap else { throw SpecError("socket path too long: \(path)") }
        withUnsafeMutableBytes(of: &addr.sun_path) { dst in
            bytes.withUnsafeBytes { src in dst.copyMemory(from: src) }
        }
        return addr
    }

    private static func noSigPipe(_ fd: Int32) {
        var one: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
    }

    static func setReceiveTimeout(_ fd: Int32, seconds: Int) {
        var tv = timeval(tv_sec: seconds, tv_usec: 0)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
    }

    /// Bind a 0600 listening socket at `path`, replacing a stale socket file.
    static func listen(path: String) throws -> Int32 {
        var addr = try address(path)
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw SpecError("socket() failed: \(String(cString: strerror(errno)))") }
        noSigPipe(fd)
        unlink(path)
        let old = umask(0o177)
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        umask(old)
        guard rc == 0 else {
            let msg = String(cString: strerror(errno))
            close(fd)
            throw SpecError("cannot bind \(path): \(msg)")
        }
        chmod(path, 0o600)
        guard Darwin.listen(fd, 8) == 0 else {
            close(fd)
            throw SpecError("listen failed on \(path)")
        }
        return fd
    }

    static func connect(path: String) throws -> Int32 {
        var addr = try address(path)
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw SpecError("socket() failed") }
        noSigPipe(fd)
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard rc == 0 else {
            close(fd)
            throw SpecError("cannot connect to \(path): \(String(cString: strerror(errno)))")
        }
        return fd
    }
}
