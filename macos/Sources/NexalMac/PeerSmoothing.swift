import Darwin
import Foundation

/// Keeps the panel steady between polls. The runtime's view of a peer can blip for a few
/// seconds (a handshake renewal, a relay reconnect, a poll that lands mid-update); showing
/// every blip makes a healthy network look like it is flapping. A peer that was connected
/// stays shown as connected for a short grace period, a missing latency keeps its last
/// reading for a while, and a peer that vanishes for one poll is kept. Real changes that
/// last longer than the grace period are shown as they are.
struct PeerSmoother {
    static let connectedGrace: TimeInterval = 20
    static let latencyGrace: TimeInterval = 90
    static let missingGrace: TimeInterval = 15

    private var lastConnectedAt: [String: Date] = [:]
    private var lastLatency: [String: (value: Double, at: Date)] = [:]
    private var lastSeen: [String: (peer: ConnectorStatus.MeshPeer, at: Date)] = [:]
    /// Round-trip times measured by the app itself (TCP connect over the tunnel), for peers
    /// the runtime reports no latency for, such as relayed ones.
    var measured: [String: (value: Double, at: Date)] = [:]

    mutating func smooth(_ status: ConnectorStatus, now: Date = Date()) -> ConnectorStatus {
        guard var mesh = status.mesh else { return status }
        var peers: [ConnectorStatus.MeshPeer] = []
        for var peer in mesh.peers {
            if peer.lifecycle == "connected" {
                lastConnectedAt[peer.id] = now
            } else if let at = lastConnectedAt[peer.id], now.timeIntervalSince(at) < Self.connectedGrace {
                peer.lifecycle = "connected"
            }
            if let latency = peer.latencyMs, latency > 0 {
                lastLatency[peer.id] = (latency, now)
            } else if peer.lifecycle == "connected" {
                if let m = measured[peer.id], now.timeIntervalSince(m.at) < Self.latencyGrace {
                    peer.latencyMs = m.value
                } else if let last = lastLatency[peer.id], now.timeIntervalSince(last.at) < Self.latencyGrace {
                    peer.latencyMs = last.value
                }
            }
            lastSeen[peer.id] = (peer, now)
            peers.append(peer)
        }
        // A peer missing from this one poll is kept briefly rather than disappearing.
        let present = Set(peers.map(\.id))
        for (id, entry) in lastSeen where !present.contains(id) && now.timeIntervalSince(entry.at) < Self.missingGrace {
            peers.append(entry.peer)
        }
        lastSeen = lastSeen.filter { now.timeIntervalSince($0.value.at) < 300 }
        // The runtime's order for present peers; a briefly missing one goes last.
        let order = Dictionary(uniqueKeysWithValues: mesh.peers.enumerated().map { ($1.id, $0) })
        mesh.peers = peers.sorted { (order[$0.id] ?? Int.max) < (order[$1.id] ?? Int.max) }
        var out = status
        out.mesh = mesh
        return out
    }
}

enum PeerLatencyProbe {
    /// Milliseconds for one TCP handshake to host:port over the tunnel. A refusal is also a full
    /// round trip, so it counts; a timeout returns nil.
    nonisolated static func connectRTT(_ host: String, port: Int, timeout: TimeInterval = 2) -> Double? {
        var addr = sockaddr_in()
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = in_port_t(UInt16(port).bigEndian)
        guard inet_pton(AF_INET, host, &addr.sin_addr) == 1 else { return nil }
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { return nil }
        defer { close(fd) }
        _ = fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) | O_NONBLOCK)
        let start = DispatchTime.now()
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        if rc != 0 {
            guard errno == EINPROGRESS else { return nil }
            var pfd = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
            guard poll(&pfd, 1, Int32(timeout * 1000)) == 1 else { return nil }
            var err: Int32 = 0; var len = socklen_t(MemoryLayout<Int32>.size)
            getsockopt(fd, SOL_SOCKET, SO_ERROR, &err, &len)
            guard err == 0 || err == ECONNREFUSED else { return nil }
        }
        return Double(DispatchTime.now().uptimeNanoseconds - start.uptimeNanoseconds) / 1_000_000
    }

    /// A port worth probing for a peer: one of its advertised services, else SSH.
    static func port(for services: [String]?) -> Int {
        let s = services ?? []
        if s.contains("ssh") { return 22 }
        if s.contains("vnc") { return 5900 }
        if s.contains("smb") { return 445 }
        return 22
    }
}
