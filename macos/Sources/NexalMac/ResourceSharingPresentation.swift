import Foundation

/// Whether this Mac is contributing resources (HARDENING-PLAN §26.3). Kept
/// strictly separate from the RDMA indicator: pausing a contribution must never
/// look like losing hardware, and losing a fast transport must never look like
/// stopping contribution. Per §26.6 this is the only one of the two indicators
/// permitted to signal a real loss of function.
///
/// Presentation only. Go remains authoritative for admission; nothing here
/// grants, implies or records consent.
struct ResourceSharingPresentation: Equatable {
    enum State {
        /// Opt-in is on and the connector reports no blocker.
        case sharing
        /// Opt-in is on and the connector is waiting on its own gate.
        case waiting
        /// The owner paused. A real, owner-chosen loss of function.
        case pausedByOwner
        /// Telemetry is not known, so Go admission fails closed.
        case telemetryUnknown
        /// No status has been read from a connector.
        case notConnected
    }

    /// Existing honesty strings, unchanged. They are constants here so the view
    /// cannot quietly reword them and the tests can assert on them.
    static let telemetryUnknown = "Telemetry unknown — Go admission fails closed"
    static let synthetic = "Synthetic development telemetry"
    static let optInCaveat = """
        Opt-in permits the Go connector to apply its resource and idle policies. \
        It does not promise that a workload is available or enabled.
        """

    /// The existing owner-activity line, verbatim, moved out of the view so the
    /// exact wording is asserted by a test instead of living in a nested
    /// ternary. Returns nil when the connector reported no telemetry at all,
    /// which is what the previous layout also did.
    static func ownerActivityLine(status: ConnectorStatus?) -> String? {
        guard let telemetry = status?.telemetry else { return nil }
        if telemetry.synthetic { return synthetic }
        if !telemetry.known { return telemetryUnknown }
        guard telemetry.ownerActive else { return "Owner idle" }
        return status?.ownerActivityOverride == true
            ? "Owner active; private work explicitly permitted"
            : "Owner active; owner priority applies"
    }

    let state: State
    /// Non-nil only while the connector reports synthetic telemetry.
    let syntheticNotice: String?
    private let blocker: String?

    init(status: ConnectorStatus?) {
        let telemetry = status?.telemetry
        syntheticNotice = telemetry?.synthetic == true ? Self.synthetic : nil
        let reportedBlocker = status?.executionBlocker?.trimmingCharacters(in: .whitespacesAndNewlines)
        blocker = (reportedBlocker?.isEmpty == false) ? reportedBlocker : nil
        guard let status else {
            state = .notConnected
            return
        }
        // Owner intent first: a paused connector is paused whatever else is true.
        if status.paused {
            state = .pausedByOwner
        } else if telemetry == nil || telemetry?.known != true {
            state = .telemetryUnknown
        } else if blocker != nil {
            state = .waiting
        } else {
            state = .sharing
        }
    }

    var indicator: IndicatorState {
        IndicatorState(
            heading: "Resources shareable",
            label: label,
            systemImage: symbolName,
            tone: tone,
            reason: reason,
            detail: nil)
    }

    var label: String {
        switch state {
        case .sharing: return "Shared with your policy applied"
        case .waiting: return "Shared — the connector is waiting"
        case .pausedByOwner: return "Paused by you"
        case .telemetryUnknown: return "Not confirmed"
        case .notConnected: return "Not connected"
        }
    }

    private var tone: IndicatorTone {
        switch state {
        case .sharing, .waiting: return .colour
        case .pausedByOwner, .telemetryUnknown, .notConnected: return .grey
        }
    }

    private var symbolName: String {
        switch state {
        case .sharing: return "checkmark.circle.fill"
        case .waiting: return "clock"
        case .pausedByOwner: return "pause.circle.fill"
        case .telemetryUnknown: return "questionmark.circle"
        case .notConnected: return "circle.dashed"
        }
    }

    private var reason: String {
        switch state {
        case .sharing:
            return "The connector reports no blocker. Execution still requires its admission and release gates."
        case .waiting:
            // Same wording the panel used before the redesign.
            return "Waiting: \(blocker ?? "")"
        case .pausedByOwner:
            return "Paused by the owner; the Go connector cancels active work."
        case .telemetryUnknown:
            return Self.telemetryUnknown
        case .notConnected:
            return "No connector status has been read. Nothing is shared and nothing is assumed."
        }
    }
}
