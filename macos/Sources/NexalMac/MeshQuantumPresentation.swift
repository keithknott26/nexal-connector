import Foundation

/// Labels only evidence from the runtime with an enforced session-generation gate.
/// An enabled flag, old install-only profile, or stale snapshot cannot earn a label.
enum MeshQuantumPresentation {
    static func label(peers: [ConnectorStatus.MeshPeer], now: Date = Date()) -> String {
        guard !peers.isEmpty, peers.allSatisfy({ peer in
            guard peer.pq == "protected", peer.lifecycle == "connected",
                  peer.quantumProfile == "nexal-mlkem1024-tcp-v2",
                  let installed = timestamp(peer.pqVerifiedAt),
                  let expires = timestamp(peer.pqExpiresAt) else { return false }
            return installed <= now && now.timeIntervalSince(installed) <= 120 &&
                expires > now && expires > installed && expires.timeIntervalSince(installed) <= 180
        }) else { return "Not reported" }
        return "ML-KEM-1024 · NIST PQC Category 5 (experimental)"
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
