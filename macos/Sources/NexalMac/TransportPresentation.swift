import Foundation

/// The transport mechanism the connector reports as actually established
/// (HARDENING-PLAN §26.3). Resolution order is RDMA, then TCP private-LAN, then
/// relayed/coordinator-mediated, and the first one actually established wins.
///
/// `unknown` is a distinct state from "no data-plane path", and neither implies
/// capability: the Go connector fails closed on unknown telemetry and this
/// mirrors it. The UI never prints "Thunderbolt 5 detected" (§26.2).
enum TransportMechanism {
    case nativeThunderboltRDMA
    case tcpPrivateLAN
    case coordinatorMediated
    /// The connector has a control-plane heartbeat only; no data-plane transport
    /// is established. This is not a claim about what the hardware could do.
    case controlPlaneOnly
    case unknown
}

/// Resolves the connector's own `transport` string into a mechanism, and keeps
/// that string visible verbatim. The raw text carries its own warnings — for
/// example "NOT protected by incoming PQ tunnel" — so it is displayed, never
/// summarised away.
struct TransportPresentation {
    let mechanism: TransportMechanism
    /// Exactly what the Go connector said, or nil when it said nothing.
    let connectorReported: String?

    /// Words that mark a mechanism as absent rather than established. A string
    /// that merely *mentions* RDMA must never be read as an RDMA link.
    private static let absent = ["unavailable", "unsupported", "not available",
                                 "not established", "incapable", "disabled",
                                 "no rdma", "without rdma", "fallback"]

    init(status: ConnectorStatus?) {
        let raw = status?.transport?.trimmingCharacters(in: .whitespacesAndNewlines)
        connectorReported = (raw?.isEmpty == false) ? raw : nil
        guard let text = connectorReported?.lowercased() else {
            mechanism = .unknown
            return
        }
        let denied = Self.absent.contains { text.contains($0) }
        if text.contains("heartbeat-only") || text.contains("heartbeat only") {
            mechanism = .controlPlaneOnly
        } else if !denied && (text.contains("rdma") || text.contains("thunderbolt")) {
            mechanism = .nativeThunderboltRDMA
        } else if !denied && (text.contains("private-lan") || text.contains("private lan")
                              || (text.contains("tcp") && text.contains("lan"))) {
            mechanism = .tcpPrivateLAN
        } else if text.contains("relay") || text.contains("coordinator")
                    || text.contains("http") || text.contains("quic") {
            mechanism = .coordinatorMediated
        } else {
            mechanism = .unknown
        }
    }

    /// The product names fixed by §26.2 and the transport research document.
    var label: String {
        switch mechanism {
        case .nativeThunderboltRDMA: return "Native Thunderbolt RDMA"
        case .tcpPrivateLAN: return "TCP private-LAN transport"
        case .coordinatorMediated: return "Relayed through the coordinator"
        case .controlPlaneOnly: return "No data transport established"
        case .unknown: return "Transport unknown"
        }
    }

    var indicator: IndicatorState {
        IndicatorState(
            heading: "Active transport",
            label: label,
            systemImage: symbolName,
            tone: tone,
            reason: reason,
            detail: detail)
    }

    private var symbolName: String {
        switch mechanism {
        case .nativeThunderboltRDMA: return "bolt.horizontal.circle.fill"
        case .tcpPrivateLAN: return "network"
        case .coordinatorMediated: return "antenna.radiowaves.left.and.right"
        case .controlPlaneOnly: return "dot.radiowaves.left.and.right"
        case .unknown: return "questionmark.circle"
        }
    }

    private var tone: IndicatorTone {
        switch mechanism {
        case .nativeThunderboltRDMA, .tcpPrivateLAN, .coordinatorMediated: return .colour
        case .controlPlaneOnly, .unknown: return .grey
        }
    }

    /// Always includes the connector's own words, so nothing the connector
    /// warned about is lost when the row replaces the old plain-text line.
    private var reason: String {
        guard let connectorReported else {
            return "The connector reported no transport. Unknown is not the same as unavailable, and nothing is assumed from it."
        }
        return "Connector reports: \(connectorReported)"
    }

    private var detail: String? {
        switch mechanism {
        case .nativeThunderboltRDMA:
            return """
                Native Thunderbolt RDMA is point-to-point over a direct cable and \
                supports send/receive only, with no hardware-initiated remote writes. \
                It is a fast message path between two Macs, not another Mac's RAM \
                appearing as local memory. Throughput is a property of the link, not \
                of this host.
                """
        case .tcpPrivateLAN:
            return """
                Private-LAN TCP is the general case: it works over a switch or \
                Wi-Fi, where a direct Thunderbolt cable does not exist. Resources \
                are shared over it exactly as they would be over RDMA; only the \
                speed differs.
                """
        case .coordinatorMediated:
            return """
                Traffic is mediated by the coordinator rather than by a direct path \
                to a peer. A relayed path is not an accelerated one, and a relay can \
                observe traffic patterns — sizes, timing and peer pairs — even when \
                it cannot read content.
                """
        case .controlPlaneOnly:
            return """
                The connector is reachable and heartbeating, and no data-plane \
                transport has been established. This describes what is running now; \
                it makes no claim about what this Mac's hardware supports.
                """
        case .unknown:
            return """
                The connector did not report a transport this app recognises. The \
                string above is shown unchanged. Unknown is reported as unknown: the \
                Go connector fails closed on unknown telemetry and this row does the \
                same rather than inferring a capability.
                """
        }
    }
}
