import Foundation

/// Whether native Thunderbolt RDMA is available on this Mac, from a probe rather
/// than from a setting (HARDENING-PLAN §26.2). Per Apple TN3205, as recorded in
/// the platform repository's transport research, native Thunderbolt RDMA needs
/// Apple silicon, Thunderbolt 5 and macOS 26.2 or later.
///
/// This app can establish two of those three facts locally: the running macOS
/// version and whether it is an Apple-silicon binary. It cannot enumerate the
/// Thunderbolt generation, and the Go connector reports no link capability, so
/// that part stays `unknown` rather than being assumed in either direction.
///
/// There is no switch here and there never will be: transport selection is
/// automatic (§26.6). Grey means slower, not broken, and none of the copy below
/// suggests buying different hardware.
struct RDMAAvailability: Equatable {
    enum State {
        /// A native Thunderbolt RDMA link is established and reported as such.
        case available
        /// A requirement is definitely not met. Sharing continues over TCP/IP.
        case unavailable
        /// Not determinable here. Never rendered as capability.
        case unknown
    }

    let state: State
    let reason: String
    let explanation: String

    /// The requirement text, stated once and reused, so the panel and the tests
    /// cannot drift apart on what RDMA actually needs.
    static let requirement = """
        Native Thunderbolt RDMA needs Apple silicon, Thunderbolt 5 and macOS 26.2 \
        or later (Apple TN3205). It is point-to-point over a direct cable and \
        supports send/receive only.
        """
    /// §26.6, in the UI's own words. Repeated in every non-available state.
    static let speedNotCapability =
        "This is a speed difference, not a loss of function: resources are shared either way."

    init(osMajor: Int, osMinor: Int, isAppleSiliconBuild: Bool, establishedLink: Bool) {
        if establishedLink {
            state = .available
            reason = "A native Thunderbolt RDMA link is established."
            explanation = Self.requirement
            return
        }
        // The OS requirement is checkable at runtime and is the one requirement
        // this app can rule out with certainty. LSMinimumSystemVersion is 14.0,
        // so this is never a compile-time assumption.
        if osMajor < 26 || (osMajor == 26 && osMinor < 2) {
            state = .unavailable
            reason = "Not available on macOS \(osMajor).\(osMinor). Sharing continues over TCP/IP. "
                + Self.speedNotCapability
            explanation = Self.requirement
                + " This Mac runs macOS \(osMajor).\(osMinor), so the operating-system"
                + " requirement is not met. The Thunderbolt generation is not probed by"
                + " this app, and no RDMA link is claimed."
            return
        }
        if !isAppleSiliconBuild {
            state = .unknown
            reason = "Not determined: this app is not running as an Apple-silicon binary. "
                + Self.speedNotCapability
            explanation = Self.requirement
                + " A translated binary cannot confirm the Apple-silicon requirement, so"
                + " availability is reported as unknown rather than guessed. Unknown is"
                + " not the same as unavailable."
            return
        }
        state = .unknown
        reason = "Not determined: no Thunderbolt RDMA link is reported on this Mac. "
            + Self.speedNotCapability
        explanation = Self.requirement
            + " macOS 26.2 or later is running, and neither this app nor the Go connector"
            + " probes the Thunderbolt generation or an RDMA link, so availability is"
            + " reported as unknown. Unknown is not the same as unavailable, and it is"
            + " never presented as capability."
    }

    /// Reads the running system. `establishedLink` comes from the resolved
    /// transport, so an active RDMA link is reported from evidence and not from
    /// a configured intention.
    static func probe(processInfo: ProcessInfo = .processInfo,
                      establishedLink: Bool = false) -> RDMAAvailability {
        let version = processInfo.operatingSystemVersion
        return RDMAAvailability(osMajor: version.majorVersion,
                                osMinor: version.minorVersion,
                                isAppleSiliconBuild: isAppleSiliconBuild,
                                establishedLink: establishedLink)
    }

    /// Build architecture, not a marketing claim. A non-arm64 build errs toward
    /// `unknown`, which is the fail-closed direction.
    static let isAppleSiliconBuild: Bool = {
        #if arch(arm64)
        return true
        #else
        return false
        #endif
    }()

    var indicator: IndicatorState {
        IndicatorState(
            heading: "Thunderbolt RDMA",
            label: label,
            systemImage: symbolName,
            tone: state == .available ? .colour : .grey,
            reason: reason,
            detail: explanation)
    }

    /// Never an error word. A grey RDMA row is the normal row.
    var label: String {
        switch state {
        case .available: return "Available on this Mac"
        case .unavailable: return "Not on this Mac — sharing at TCP/IP speed"
        case .unknown: return "Unknown — sharing at TCP/IP speed"
        }
    }

    private var symbolName: String {
        switch state {
        case .available: return "bolt.horizontal.circle.fill"
        case .unavailable: return "bolt.horizontal.circle"
        case .unknown: return "questionmark.circle"
        }
    }
}
