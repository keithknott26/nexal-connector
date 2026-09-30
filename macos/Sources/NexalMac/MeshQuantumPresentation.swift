import Foundation

/// Labels only evidence from the runtime with an enforced session-generation gate.
/// An enabled flag, old install-only profile, or stale snapshot cannot earn a label.
enum MeshQuantumPresentation {
    /// With several peers, only the neXal gateway links ("gw-…") decide the label, matching
    /// the connector's host-level claim. A single peer is judged on its own link.
    static func label(peers: [ConnectorStatus.MeshPeer], now: Date = Date()) -> String {
        let gateways = peers.filter { $0.name.lowercased().hasPrefix("gw-") && $0.lifecycle == "connected" }
        let judged = peers.count == 1 ? peers : gateways
        guard !judged.isEmpty, judged.allSatisfy({ peer in
            guard peer.pq == "protected", peer.lifecycle == "connected",
                  peer.quantumProfile == "nexal-mlkem1024-tcp-v2",
                  let installed = timestamp(peer.pqVerifiedAt),
                  let expires = timestamp(peer.pqExpiresAt) else { return false }
            return installed <= now && now.timeIntervalSince(installed) <= 120 &&
                expires > now && expires > installed && expires.timeIntervalSince(installed) <= 180
        }) else { return "Not reported" }
        return "🔐 Level 5 · ML-KEM-1024"
    }

    private static func timestamp(_ text: String?) -> Date? {
        guard let text else { return nil }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: text) { return date }
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: text)
    }
}
