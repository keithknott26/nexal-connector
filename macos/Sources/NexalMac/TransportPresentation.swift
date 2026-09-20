import Foundation

/// Presents the active transport row (HARDENING-PLAN §26.3) from the facts a
/// reporting layer supplied. It reads a `TransportCapability` and nothing else —
/// no status field, no process, no network — so the row follows the seam rather
/// than the current source, and every state is testable without a Mac.
struct TransportPresentation: Equatable {
    let capability: TransportCapability

    init(capability: TransportCapability) {
        self.capability = capability
    }

    var mechanism: TransportMechanism { capability.mechanism }

    /// The product names fixed by §26.2 and the transport research document.
    /// Never "Thunderbolt 5 detected", and never a name for a path that is not
    /// established.
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

    /// Always includes the reporting layer's own words when there are any, so
    /// nothing it warned about is lost now that the row replaced plain text.
    private var reason: String {
        guard let reported = capability.reportedText, !reported.isEmpty else {
            return "No transport is being reported. Unknown is not the same as unavailable, and nothing is assumed from it."
        }
        return "Reported by \(capability.sourceName): \(reported)"
    }

    private var detail: String? {
        switch mechanism {
        case .nativeThunderboltRDMA:
            return """
                Native Thunderbolt RDMA is point-to-point over a direct cable and \
                supports send/receive only, with no hardware-initiated remote writes. \
                It is a fast message path between two Macs, not another Mac's RAM \
                appearing as local memory. Throughput is a property of the link, not \
                of this host, so it is reported per link and never as an RDMA badge \
                for the whole Mac.
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
                No recognised transport report is available, so the row says unknown \
                and stops there. Unknown is reported as unknown: the Go connector \
                fails closed on unknown telemetry and this row does the same rather \
                than inferring a path or a capability. Any text a reporting layer \
                does supply is shown above unchanged.
                """
        }
    }
}
