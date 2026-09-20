import Foundation

/// Presents the RDMA availability row (HARDENING-PLAN §26.3) from reported
/// facts. It reads a `TransportCapability` and nothing else: availability is
/// supplied by the layers that own each subsystem, and is not measured, probed
/// or inferred here.
///
/// There is one row, and the facts behind it are per-subsystem (§29.8): a
/// collective backend that moves tensors between inference ranks is not a
/// general-purpose pipe, so a memory pager cannot use it, and the two can
/// differ. The row therefore summarises and then itemises, rather than
/// collapsing two facts into one light or adding a second top-level indicator.
///
/// Grey is designed as a first-class, informative state, because it is what
/// nearly every Mac shows: per Apple TN3205 native Thunderbolt RDMA needs Apple
/// silicon, Thunderbolt 5 and macOS 26.2 or later, and today nothing reports on
/// either subsystem, so `unknown` is the normal state rather than an edge case. A
/// grey row means *slower, not unavailable* (§26.6): it is never drawn as an
/// error, a warning or a loss of function, and it never suggests different
/// hardware.
///
/// There is no switch here and there never will be — transport selection is
/// automatic.
struct RDMAPresentation: Equatable {
    /// What the single row says, derived from the per-subsystem facts.
    enum Summary: Equatable {
        /// Every subsystem is reported available.
        case availableForAll
        /// At least one subsystem is reported available and at least one is not.
        case availableForSome
        /// Every subsystem is positively reported as having no RDMA path.
        case unavailableForAll
        /// Nothing is available and something is unreported. The usual state.
        case unknown
    }

    let capability: TransportCapability

    init(capability: TransportCapability) {
        self.capability = capability
    }

    /// The documented requirement, stated once and reused, so the panel and the
    /// tests cannot drift apart on what RDMA actually needs.
    static let requirement = """
        Native Thunderbolt RDMA needs Apple silicon, Thunderbolt 5 and macOS 26.2 \
        or later (Apple TN3205). It is point-to-point over a direct cable and \
        supports send/receive only.
        """
    /// §26.6, in the UI's own words. Present in every non-available state.
    static let speedNotCapability =
        "This is a speed difference, not a loss of function: resources are shared either way."
    /// §29.8, in the UI's own words. Shown wherever the breakdown is shown.
    static let perSubsystem = """
        RDMA is per path, not one setting. Each path below is reported by the layer \
        that owns it, and one can be available while the other is not.
        """

    var summary: Summary {
        let available = capability.subsystems(in: .available)
        if available.count == TransportSubsystem.allCases.count { return .availableForAll }
        if !available.isEmpty { return .availableForSome }
        if capability.subsystems(in: .unavailable).count == TransportSubsystem.allCases.count {
            return .unavailableForAll
        }
        return .unknown
    }

    /// Never an error word. A grey RDMA row is the ordinary row.
    var label: String {
        switch summary {
        case .availableForAll: return "Available on this Mac"
        case .availableForSome: return "Available for some paths"
        case .unavailableForAll: return "Not on this Mac — sharing at TCP/IP speed"
        case .unknown: return "Unknown — sharing at TCP/IP speed"
        }
    }

    var reason: String {
        switch summary {
        case .availableForAll:
            return "Every path is reported as having native Thunderbolt RDMA available."
        case .availableForSome:
            let names = capability.subsystems(in: .available).map(\.title).joined(separator: ", ")
            return "Reported available for \(names). Other paths run at TCP/IP speed. "
                + Self.speedNotCapability
        case .unavailableForAll:
            return "Reported as having no RDMA path here. Sharing continues over TCP/IP. "
                + Self.speedNotCapability
        case .unknown where capability.sourceReporting:
            return "Not reported for either path, so it stays unknown. " + Self.speedNotCapability
        case .unknown:
            return "No layer is reporting RDMA availability yet, so it stays unknown. "
                + Self.speedNotCapability
        }
    }

    var explanation: String {
        switch summary {
        case .availableForAll, .availableForSome:
            return Self.requirement + " " + Self.perSubsystem
                + " Throughput is a property of a link, not of a host, so it is reported"
                + " per link and only ever from measurements taken here."
        case .unavailableForAll:
            return Self.requirement + " " + Self.perSubsystem
                + " Resource sharing is unaffected; it runs over private-LAN TCP instead."
        case .unknown:
            return Self.requirement + " " + Self.perSubsystem
                + " Availability is supplied by the layers that own each path, and is not"
                + " measured in this app. Nothing is reporting it yet, so this row says"
                + " unknown deliberately: unknown is not the same as unavailable, and"
                + " absence of a report is never read as absence of capability. Resource"
                + " sharing does not depend on it."
        }
    }

    /// One line per subsystem, in a fixed order, always all of them — so a
    /// reader can see which path a state belongs to instead of guessing.
    var breakdown: [String] {
        TransportSubsystem.allCases.map { subsystem in
            "\(subsystem.title): \(Self.word(for: capability.rdmaState(for: subsystem))). \(subsystem.scope)"
        }
    }

    /// Plain words for one subsystem's state. No error vocabulary.
    static func word(for state: TransportCapability.RDMAState) -> String {
        switch state {
        case .available: return "reported available"
        case .unavailable: return "reported as not available here"
        case .unknown: return "unknown, not reported yet"
        }
    }

    var indicator: IndicatorState {
        IndicatorState(
            heading: "Thunderbolt RDMA",
            label: label,
            systemImage: symbolName,
            tone: summary == .unavailableForAll || summary == .unknown ? .grey : .colour,
            reason: reason,
            detail: explanation,
            detailLines: breakdown)
    }

    private var symbolName: String {
        switch summary {
        case .availableForAll: return "bolt.horizontal.circle.fill"
        case .availableForSome: return "bolt.horizontal.circle"
        case .unavailableForAll: return "bolt.horizontal.circle"
        case .unknown: return "questionmark.circle"
        }
    }
}
