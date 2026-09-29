import Foundation

/// The transport mechanism reported as actually established (HARDENING-PLAN
/// §26.3). Resolution order is RDMA, then TCP private-LAN, then
/// relayed/coordinator-mediated, and the first one actually established wins.
///
/// `unknown` is a distinct state from "no data path established", and neither
/// implies capability: the Go connector fails closed on unknown telemetry and
/// this mirrors it. The UI never prints "Thunderbolt 5 detected" (§26.2).
enum TransportMechanism {
    case nativeThunderboltRDMA
    case tcpPrivateLAN
    case coordinatorMediated
    /// A control-plane heartbeat only; no data-plane transport is established.
    /// This is not a claim about what the hardware could do.
    case controlPlaneOnly
    case unknown

    /// Words that mark a mechanism as absent rather than established. Text that
    /// merely *mentions* RDMA must never be read as an established RDMA link.
    private static let absent = ["unavailable", "unsupported", "not available",
                                 "not established", "incapable", "disabled",
                                 "no rdma", "without rdma", "fallback"]

    /// Classifies a reporting layer's own words. Never upgrades a report: text
    /// this app does not recognise resolves to `unknown`, not to a capability.
    static func resolve(reportedText: String?) -> TransportMechanism {
        guard let text = reportedText?.lowercased(), !text.isEmpty else { return .unknown }
        let denied = absent.contains { text.contains($0) }
        if text.contains("heartbeat-only") || text.contains("heartbeat only") {
            return .controlPlaneOnly
        }
        // IP over a Thunderbolt bridge is a TCP transport that happens to run on
        // a Thunderbolt cable. It is NOT RDMA. This is checked BEFORE the RDMA
        // branch because such text mentions Thunderbolt without any denial word,
        // so an RDMA-first order classifies it as an established RDMA link.
        if !denied && (text.contains("ip over thunderbolt")
                       || text.contains("thunderbolt bridge")
                       || text.contains("thunderbolt ip")) {
            return .tcpPrivateLAN
        }
        // "rdma" is REQUIRED. A cable type is not a protocol: matching bare
        // "thunderbolt" here claimed a native RDMA link for any report that merely
        // named the cable, and resolveCapability() turns that into
        // rdma[.memoryPager] = .available -- a capability claim, not a label.
        // Thunderbolt 4 carries IP happily and cannot do RDMA at all, so the old
        // condition would assert a capability the hardware does not have.
        if !denied && text.contains("rdma") {
            return .nativeThunderboltRDMA
        }
        if !denied && (text.contains("private-lan") || text.contains("private lan")
                       || (text.contains("tcp") && text.contains("lan"))) {
            return .tcpPrivateLAN
        }
        if text.contains("relay") || text.contains("coordinator")
            || text.contains("http") || text.contains("quic") {
            return .coordinatorMediated
        }
        return .unknown
    }
}

/// The subsystems that can each have their own RDMA path (HARDENING-PLAN §29.8).
/// RDMA availability is **per subsystem, not one boolean**: MLX's collective
/// backend moves tensors between ranks and is not a general-purpose pipe, so a
/// memory pager cannot use it. The two facts can differ, and a single "RDMA: on"
/// light would be wrong.
enum TransportSubsystem: String, CaseIterable {
    /// Tensors between distributed inference ranks. Owned by the compute
    /// runtime's own collective backend, which this app does not implement.
    case computeCollectives
    /// Paged memory between Macs. A separate path that cannot use the compute
    /// collectives' RDMA even when that one is available.
    case memoryPager

    var title: String {
        switch self {
        case .computeCollectives: return "Compute collectives"
        case .memoryPager: return "Memory pager"
        }
    }

    /// Why this subsystem is separate. Stated without claiming that this app
    /// performs, owns or measures RDMA for either one.
    var scope: String {
        switch self {
        case .computeCollectives:
            return "Model data exchanged between Macs running shared AI work."
        case .memoryPager:
            return "Paged memory between Macs, over a separate path that cannot reuse the collective backend."
        }
    }
}

/// Transport and RDMA facts as **reported** to this app, never as measured by
/// it. This is the single seam the indicators and graphs depend on: swap the
/// `TransportCapabilityProviding` implementation and no view, presenter or chart
/// changes. The resource-sharing layer that owns transport is the authority for
/// every field here.
struct TransportCapability: Equatable {
    /// Whether RDMA is available, as reported. `unknown` is the state whenever
    /// nothing is reporting, which is the normal state today — not an edge case.
    enum RDMAState {
        /// A reporting source says an RDMA path is available and established.
        case available
        /// A reporting source positively says RDMA is not available here.
        /// Absence of a report is never read as absence of capability.
        case unavailable
        /// Nothing is reporting, or the report is not recognised.
        case unknown
    }

    /// Per subsystem (§29.8). A subsystem absent from the dictionary is
    /// `unknown`: silence is never read as availability or as denial.
    let rdma: [TransportSubsystem: RDMAState]
    let mechanism: TransportMechanism
    /// The reporting layer's own words, preserved verbatim for display. It may
    /// carry its own warnings, so it is shown rather than summarised away.
    let reportedText: String?
    /// How the UI attributes these facts. Short, lower-case, fits mid-sentence.
    let sourceName: String
    /// False when no layer is reporting transport capability. Then `rdma` and
    /// `mechanism` are `unknown` by construction.
    let sourceReporting: Bool

    /// Reported state for one subsystem. Unknown unless a source said otherwise.
    func rdmaState(for subsystem: TransportSubsystem) -> RDMAState {
        rdma[subsystem] ?? .unknown
    }

    func subsystems(in state: RDMAState) -> [TransportSubsystem] {
        TransportSubsystem.allCases.filter { rdmaState(for: $0) == state }
    }

    /// The honest default: nothing is reporting, so nothing is claimed for any
    /// subsystem. This is the real state today and stays so until a source
    /// exists for each one.
    static func notReported(sourceName: String) -> TransportCapability {
        TransportCapability(rdma: [:], mechanism: .unknown, reportedText: nil,
                            sourceName: sourceName, sourceReporting: false)
    }
}

/// The seam. One narrow protocol supplies every transport and RDMA fact the UI
/// shows, so the source can change without touching a view or a graph.
///
/// The only implementation today is `ConnectorStatusCapabilitySource`, which
/// reads the Go connector's status. It reports RDMA as `unknown`, because that
/// status carries no RDMA capability field — and an unknown that stays unknown
/// is correct until a layer that owns transport reports one.
///
/// A replacement implementation must supply, per call: the RDMA state **per
/// subsystem** (`available` / `unavailable` / `unknown`, never inferred from
/// silence, and never one shared flag — §29.8), the established mechanism, its
/// own words for display, a source name for attribution, and
/// `sourceReporting: false` whenever it has no fresh report. Two sources may
/// compose here, one per subsystem, since the two facts have different owners.
/// It must not publish a vendor performance claim as if measured; only numbers
/// observed on this Mac are ever charted.
protocol TransportCapabilityProviding {
    /// Named for the UI's attribution line, not for a product.
    var sourceName: String { get }

    /// Called once per status poll — never on a timer of its own — with the
    /// latest connector status, which an implementation may ignore entirely.
    func capability(from status: ConnectorStatus?) -> TransportCapability
}

/// Today's only source: the Go connector's `transport` string.
///
/// It reports an established path, not a capability. So the mechanism is
/// resolved from it, and RDMA stays `unknown` unless that very string names an
/// established RDMA path. This source never returns `unavailable`: the Go status
/// has no field that positively denies RDMA, and treating its silence as a
/// denial would be an invention.
///
/// Subsystem attribution, stated so it can be corrected: this source can only
/// speak for `.memoryPager`, because the pager's transport is the connector's
/// own and lives beside the string being read. `.computeCollectives` is owned by
/// the compute runtime's collective backend, which reports nowhere yet, so this
/// source leaves it `unknown` always rather than answering for it (§29.8).
struct ConnectorStatusCapabilitySource: TransportCapabilityProviding {
    let sourceName = "neXal@home"

    func capability(from status: ConnectorStatus?) -> TransportCapability {
        let reported = status?.transport?.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let reported, !reported.isEmpty else {
            return .notReported(sourceName: sourceName)
        }
        let mechanism = TransportMechanism.resolve(reportedText: reported)
        var rdma: [TransportSubsystem: TransportCapability.RDMAState] = [:]
        if mechanism == .nativeThunderboltRDMA {
            rdma[.memoryPager] = .available
        }
        return TransportCapability(
            rdma: rdma,
            mechanism: mechanism,
            reportedText: reported,
            sourceName: sourceName,
            sourceReporting: true)
    }
}
