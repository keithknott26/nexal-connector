import Foundation

/// Which one thing the window should show right now.
///
/// WHY THIS IS A TYPE AND NOT `if` STATEMENTS IN THE VIEW. The window used to show
/// every section at once -- status, details, graphs, controls, pairing, setup -- and
/// left the person to work out which one applied to them. The order of the stages
/// below IS the product: resume if you can, otherwise get linked, otherwise show the
/// code, and once you are on the network show the network.
///
/// Keeping it out of SwiftUI is deliberate. Swift is not compiled in the environment
/// these changes are authored in, so view code cannot be checked here at all, while a
/// plain value type like this is covered by tests that run on the macOS CI runner.
/// Putting the decision here means the part that can be wrong is the part that is
/// tested, and the view is reduced to a switch with no logic in it.
enum ConnectorStage: Equatable {
    /// No answer from the connector yet. Distinct from `.offline`: "we have not asked"
    /// must not be drawn as "it is not running", which is the kind of small lie that
    /// sends someone off debugging a working system.
    case starting

    /// The connector is not running, or this Mac has no connector chosen yet.
    case offline

    /// Running and enrolled, but not yet joined to an account. A code needs minting.
    case needsPairing

    /// A live code is on screen, waiting for the phone.
    case showingCode

    /// The phone scanned it. Terminal for the code; the network takes over from here.
    case paired

    /// The code died before a phone reached it. Recoverable by minting another.
    case pairingEnded(PairingPresentation.Status)

    /// The stage the window should be in, derived from what the last poll returned.
    ///
    /// ORDER MATTERS AND IS NOT ARBITRARY.
    ///
    /// `hasAnswered` is checked first so a slow first poll shows "starting" rather
    /// than flashing "offline" and then correcting itself -- a flash of a wrong,
    /// alarming state is worse than a moment of honest silence.
    ///
    /// Running is checked before pairing because a pairing record outlives the
    /// process that made it. A code left on screen after the connector stopped would
    /// invite someone to scan something nothing is listening for.
    ///
    /// `.scanned` is checked BEFORE liveness, so the success is reported even if the
    /// record also looks expired by the time it is read. Losing a completed pairing
    /// to a clock comparison would send the owner round the loop again for nothing.
    static func derive(hasAnswered: Bool,
                       isRunning: Bool,
                       isEnrolled: Bool,
                       pairing: PairingPresentation?,
                       now: Date) -> ConnectorStage {
        guard hasAnswered else { return .starting }
        guard isRunning else { return .offline }
        guard let pairing else { return isEnrolled ? .needsPairing : .offline }
        if pairing.status == .scanned { return .paired }
        if pairing.status.isTerminal { return .pairingEnded(pairing.status) }
        // Live by the server's own account, but the expiry has passed on this Mac.
        // Treated as ended: the phone will be refused when it scans, so continuing to
        // display the code would be showing something known not to work.
        if let expiresAt = pairing.expiresAt, expiresAt <= now { return .pairingEnded(.expired) }
        return .showingCode
    }

    /// Whether the QR code is on screen. The one question the view asks most.
    var isShowingCode: Bool { self == .showingCode }

    /// Whether this Mac is on the neXal network, and so whether network figures mean
    /// anything yet. Graphs of a network you have not joined are decoration.
    var isOnNetwork: Bool { self == .paired }

    /// The single line at the top of the window. Each names a state the person can act
    /// on, never an internal one.
    var headline: String {
        switch self {
        case .starting: return "Starting neXal"
        case .offline: return "neXal is not running"
        case .needsPairing: return "Link this Mac"
        case .showingCode: return "Scan this code with your iPhone"
        case .paired: return "On the neXal network"
        case .pairingEnded(.expired): return "That code expired"
        case .pairingEnded(.cancelled): return "Pairing was cancelled"
        case .pairingEnded: return "Pairing stopped"
        }
    }

    /// What to do next, in the second person, or nil when there is nothing to do but
    /// wait. Never speculates about a cause it cannot observe.
    var guidance: String? {
        switch self {
        case .starting:
            return nil
        case .offline:
            return "Start the connector to link this Mac to your other Macs."
        case .needsPairing:
            return "Show a code, then scan it in the neXal app on your iPhone."
        case .showingCode:
            return "Open neXal on your iPhone and scan the code. This Mac joins the same "
                + "account your iPhone is signed in to."
        case .paired:
            return nil
        case .pairingEnded(.expired):
            return "Codes are short-lived on purpose. Show a new one and scan it."
        case .pairingEnded(.cancelled):
            return "Show a new code when you are ready to link this Mac."
        case let .pairingEnded(status):
            return "The connector reported: \(status.raw). Show a new code to try again."
        }
    }
}
