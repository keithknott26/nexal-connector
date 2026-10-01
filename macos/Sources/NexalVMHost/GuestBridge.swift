import Foundation

/// Bridges the unix socket `guestSocket` to the guest's second virtio console
/// port (/dev/hvc1), as described in connector/internal/sandbox/guest.go.
/// `vmEndFD` is the host-side end of the socketpair whose other end is attached
/// to the VM's console port. One client at a time; a new connection replaces the
/// old one. Bytes the guest writes while nobody is connected are dropped.
final class GuestBridge: @unchecked Sendable {
    private let path: String
    private let vmEndFD: Int32
    private let queue = DispatchQueue(label: "nexal.vmhost.guest")
    private var listenFD: Int32 = -1
    private var acceptSource: DispatchSourceRead?
    private var vmSource: DispatchSourceRead?
    private var clientFD: Int32 = -1
    private var clientSource: DispatchSourceRead?

    init(path: String, vmEndFD: Int32) {
        self.path = path
        self.vmEndFD = vmEndFD
    }

    func start() throws {
        listenFD = try UnixSocket.listen(path: path)
        let a = DispatchSource.makeReadSource(fileDescriptor: listenFD, queue: queue)
        a.setEventHandler { [weak self] in self?.accepted() }
        a.resume()
        acceptSource = a

        let v = DispatchSource.makeReadSource(fileDescriptor: vmEndFD, queue: queue)
        v.setEventHandler { [weak self] in self?.fromGuest() }
        v.resume()
        vmSource = v
    }

    func stop() {
        queue.sync {
            dropClient()
            acceptSource?.cancel(); acceptSource = nil
            vmSource?.cancel(); vmSource = nil
            if listenFD >= 0 { close(listenFD); listenFD = -1 }
            unlink(path)
        }
    }

    private func accepted() {
        let c = accept(listenFD, nil, nil)
        guard c >= 0 else { return }
        dropClient()
        var one: Int32 = 1
        setsockopt(c, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
        clientFD = c
        let s = DispatchSource.makeReadSource(fileDescriptor: c, queue: queue)
        s.setEventHandler { [weak self] in self?.fromClient(c) }
        s.setCancelHandler { close(c) }
        s.resume()
        clientSource = s
    }

    private func dropClient() {
        clientSource?.cancel()
        clientSource = nil
        clientFD = -1
    }

    private func fromClient(_ c: Int32) {
        var buf = [UInt8](repeating: 0, count: 4096)
        let n = read(c, &buf, buf.count)
        if n <= 0 { if c == clientFD { dropClient() }; return }
        _ = GuestBridge.writeAll(vmEndFD, buf, n)
    }

    private func fromGuest() {
        var buf = [UInt8](repeating: 0, count: 4096)
        let n = read(vmEndFD, &buf, buf.count)
        if n <= 0 { vmSource?.cancel(); vmSource = nil; return }
        guard clientFD >= 0 else { return }
        if !GuestBridge.writeAll(clientFD, buf, n) { dropClient() }
    }

    /// Blocking write of the first `count` bytes; false on error.
    private static func writeAll(_ fd: Int32, _ buf: [UInt8], _ count: Int) -> Bool {
        var off = 0
        while off < count {
            let w = buf.withUnsafeBytes { write(fd, $0.baseAddress! + off, count - off) }
            if w < 0 {
                if errno == EINTR { continue }
                if errno == EAGAIN {
                    var p = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
                    if poll(&p, 1, 1000) <= 0 { return false }
                    continue
                }
                return false
            }
            off += w
        }
        return true
    }
}
