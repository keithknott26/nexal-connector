import Foundation

/// Line protocol on the 0600 control socket. One request per connection:
///   "stop\n"   -> "ok\n"      (ACPI power button, forced stop after the grace period)
///   "status\n" -> "<phase>\n" (starting | running | stopping)
///   other      -> "error: unknown command\n"
final class ControlServer: @unchecked Sendable {
    private let path: String
    private let handler: @Sendable (String) -> String
    private let queue = DispatchQueue(label: "nexal.vmhost.control")
    private var listenFD: Int32 = -1
    private var source: DispatchSourceRead?

    init(path: String, handler: @escaping @Sendable (String) -> String) {
        self.path = path
        self.handler = handler
    }

    func start() throws {
        let fd = try UnixSocket.listen(path: path)
        listenFD = fd
        let src = DispatchSource.makeReadSource(fileDescriptor: fd, queue: queue)
        src.setEventHandler { [weak self] in self?.acceptOne() }
        src.resume()
        source = src
    }

    func stop() {
        source?.cancel()
        source = nil
        if listenFD >= 0 { close(listenFD); listenFD = -1 }
        unlink(path)
    }

    private func acceptOne() {
        let c = accept(listenFD, nil, nil)
        guard c >= 0 else { return }
        defer { close(c) }
        UnixSocket.setReceiveTimeout(c, seconds: 2)
        var buf = [UInt8](repeating: 0, count: 256)
        let n = read(c, &buf, buf.count)
        guard n > 0 else { return }
        let line = String(decoding: buf[0..<n], as: UTF8.self)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        let reply = handler(line) + "\n"
        _ = reply.withCString { write(c, $0, strlen($0)) }
    }
}

enum ControlClient {
    /// Send one command and return the trimmed reply.
    static func send(path: String, command: String) throws -> String {
        let fd = try UnixSocket.connect(path: path)
        defer { close(fd) }
        UnixSocket.setReceiveTimeout(fd, seconds: 5)
        let line = command + "\n"
        let w = line.withCString { write(fd, $0, strlen($0)) }
        guard w > 0 else { throw SpecError("cannot write to control socket") }
        var out = Data()
        var buf = [UInt8](repeating: 0, count: 256)
        while true {
            let n = read(fd, &buf, buf.count)
            if n <= 0 { break }
            out.append(contentsOf: buf[0..<n])
            if out.last == UInt8(ascii: "\n") { break }
        }
        guard !out.isEmpty else { throw SpecError("no reply from control socket") }
        return String(decoding: out, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
