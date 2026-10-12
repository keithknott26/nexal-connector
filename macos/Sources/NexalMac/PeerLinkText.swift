import Foundation

/// The speed summary next to a peer's name: "⚡️ 81 ms @ 104 Mbps", or "⚡️ 81 ms" when no bandwidth
/// has been measured. (The ⚡️ marks a direct link.)
enum PeerLinkText {
    static func summary(direct: Bool, latencyMs: Double, bandwidthMbps: Double?) -> String {
        var text = (direct ? "⚡️ " : "") + latencyMs.formatted(.number.precision(.fractionLength(0))) + " ms"
        if let bandwidth = bandwidthText(bandwidthMbps) { text += " @ " + bandwidth }
        return text
    }

    /// nil when there is no usable reading.
    static func bandwidthText(_ mbps: Double?) -> String? {
        guard let mbps, mbps.isFinite, mbps > 0 else { return nil }
        if mbps >= 1000 { return (mbps / 1000).formatted(.number.precision(.fractionLength(1))) + " Gbps" }
        if mbps >= 10 { return mbps.formatted(.number.precision(.fractionLength(0))) + " Mbps" }
        return mbps.formatted(.number.precision(.fractionLength(1))) + " Mbps"
    }
}
