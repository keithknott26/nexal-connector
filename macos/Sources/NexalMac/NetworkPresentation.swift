import Foundation

/// The entire menu bar, as a value.
///
/// The panel previously rendered activity graphs, owner controls, a manual job
/// acceptance window, a contribution toggle, a connector picker, a coordinator text
/// field, a development-environment checkbox and a pairing role picker -- all at once,
/// on a Mac that had not joined anything yet. The point of this app is to get a Mac onto
/// the neXal network, so before that has happened there is exactly one thing to offer.
///
/// Rendering decisions live here rather than in the view because nothing in the view can
/// be tested: SwiftUI bodies need a running app, and there is no Swift compiler in the
/// environment this was written in. A view that is a thin `switch` over these cases is
/// reviewable by reading it; a view that computes its own state is not.
enum NetworkScreen: Equatable {
    /// The connector binary or configuration is missing. Nothing else is offered,
    /// because nothing else can work until it is resolved.
    case needsConnector(reason: String)
    /// Not yet part of the network: one button, nothing else.
    case notLinked
    /// A pairing code is live and the Mac is waiting to be claimed from the phone.
    case linking(LinkingState)
    /// Linked. Tunnel indicator plus the other Macs.
    case linked(NetworkState)

    static func derive(connectorProblem: String?,
                       isLinked: Bool,
                       pairing: LinkingState?,
                       network: NetworkState) -> NetworkScreen {
        if let problem = connectorProblem, !problem.isEmpty { return .needsConnector(reason: problem) }
        if isLinked { return .linked(network) }
        // An active pairing outranks "not linked" only while it is still usable; an
        // expired code must not leave the panel showing a QR nobody can scan.
        if let pairing, !pairing.isExpired { return .linking(pairing) }
        return .notLinked
    }
}

struct LinkingState: Equatable {
    let pairingId: String
    let secondsRemaining: Int
    /// Rendered as a QR code for the iOS app to scan.
    let payload: String

    var isExpired: Bool { secondsRemaining <= 0 }

    /// Deliberately not a percentage or a spinner: the phone may be claimed at any
    /// moment, and a progress bar would imply a duration this does not have.
    var statusLine: String {
        isExpired ? "Code expired — generate a new one"
                  : "Scan with the neXal app on your iPhone — \(secondsRemaining)s left"
    }
}

/// Whether the post-quantum tunnel is actually up.
///
/// The old panel never said. `nexal status` has carried `pq {configured, verified,
/// protocol}` the whole time and nothing rendered it, so the one fact the user most
/// wanted was the one fact the UI omitted.
enum TunnelIndicator: Equatable {
    case notConfigured
    case connecting
    /// QUIC negotiated AND a recognised hybrid key-agreement group observed.
    case quantumSafe(group: String)
    /// Connected, but no hybrid group has been observed. QUIC alone proves nothing
    /// about post-quantum negotiation, so this must never render as a success state.
    case connectedNotConfirmed(protocolName: String)
    /// cloudflared reported something unsafe or ambiguous. The agent cancels work in
    /// this state, so it is an error, not a warning.
    case quarantined

    static func derive(configured: Bool, connected: Bool, quarantined: Bool,
                       protocolName: String, keyAgreement: String) -> TunnelIndicator {
        if quarantined { return .quarantined }
        if !configured { return .notConfigured }
        if !connected { return .connecting }
        // Only the two groups internal/tunnel is willing to recognise. Anything else
        // it quarantines, so anything else reaching here is not a hybrid group.
        if keyAgreement == "X25519MLKEM768" || keyAgreement == "X25519Kyber768Draft00" {
            return .quantumSafe(group: keyAgreement)
        }
        return .connectedNotConfirmed(protocolName: protocolName.isEmpty ? "quic" : protocolName)
    }

    var title: String {
        switch self {
        case .notConfigured:             return "Quantum-safe tunnel not configured"
        case .connecting:                return "Establishing quantum-safe tunnel…"
        case .quantumSafe:               return "Quantum-safe tunnel established"
        case .connectedNotConfirmed:     return "Tunnel up — post-quantum NOT confirmed"
        case .quarantined:               return "Tunnel quarantined — work stopped"
        }
    }

    var detail: String {
        switch self {
        case .notConfigured:
            return "This Mac has no tunnel configured, so it is reachable only on the local network."
        case .connecting:
            return "Waiting for cloudflared to report a connection."
        case .quantumSafe(let group):
            return "QUIC with \(group) hybrid key agreement."
        case .connectedNotConfirmed(let proto):
            return "Connected over \(proto), but no hybrid key-agreement group has been observed. "
                 + "A QUIC connection alone is not evidence of post-quantum protection."
        case .quarantined:
            return "cloudflared reported an unsafe or ambiguous state — a protocol fallback, "
                 + "post-quantum disabled, or output that could not be trusted. Restart to clear it."
        }
    }

    /// Drives the dot colour. Only a confirmed hybrid group is green: a tunnel that is
    /// merely up must not look identical to one that is verifiably protected.
    var severity: IndicatorSeverity {
        switch self {
        case .quantumSafe:           return .good
        case .connecting:            return .pending
        case .notConfigured:         return .inactive
        case .connectedNotConfirmed: return .warning
        case .quarantined:           return .bad
        }
    }
}

enum IndicatorSeverity: Equatable { case good, pending, warning, bad, inactive }

/// How a peer can actually be reached, which is not the same as which addresses it
/// advertised.
enum PeerReachability: Equatable {
    /// An address on a network this Mac is also on. This is the only case where an
    /// ssh or vnc target is real.
    case sameNetwork(address: String, port: UInt16)
    /// The peer is linked but not on this network. The coordinator's observed `wan`
    /// address is the NAT router the advertisement arrived from, NOT the Mac, so
    /// offering it as an ssh target would be offering an address that cannot work.
    case remoteNoRoute
    case unknown

    var isConnectable: Bool { if case .sameNetwork = self { return true }; return false }
}

struct NetworkPeer: Equatable, Identifiable {
    let id: String
    let name: String
    let reachability: PeerReachability

    /// The address to show, or nil when there is nothing honest to show.
    var displayAddress: String? {
        if case .sameNetwork(let address, _) = reachability { return address }
        return nil
    }

    /// Copyable ssh target, only when a route genuinely exists.
    func sshCommand(user: String) -> String? {
        guard case .sameNetwork(let address, _) = reachability else { return nil }
        return "ssh \(user)@\(address)"
    }

    var statusNote: String {
        switch reachability {
        case .sameNetwork:  return "On this network"
        case .remoteNoRoute:
            return "Linked, but not reachable from here: no private route to this Mac exists yet."
        case .unknown:      return "Address not advertised yet"
        }
    }
}

struct NetworkState: Equatable {
    let tunnel: TunnelIndicator
    let peers: [NetworkPeer]
    let thisHostName: String

    var connectablePeers: [NetworkPeer] { peers.filter { $0.reachability.isConnectable } }

    /// Shown when peers exist but none can be reached, so an empty-looking list is
    /// explained rather than just empty.
    var unreachableNote: String? {
        guard !peers.isEmpty, connectablePeers.isEmpty else { return nil }
        return "No Mac on this network right now. Remote Macs need a private network route "
             + "before ssh or VNC can reach them."
    }

    var emptyNote: String? {
        peers.isEmpty ? "No other Macs linked yet. Link another Mac with the neXal app." : nil
    }
}

/// Where a "Leave neXal network" request is.
///
/// The old button opened a `confirmationDialog`, which a `MenuBarExtra` window does
/// not reliably present, so the click looked dead: no question, no progress, no
/// result. Every phase is now rendered inside the panel itself, and the menu bar
/// glyph changes while the leave is in flight, so the owner always sees that the
/// click registered and what happened.
enum LeavePhase: Equatable {
    /// Asked inline, next to the button, rather than in a dialog.
    case confirming
    /// Waiting for an in-flight status check and stopping pairing activity.
    case preparing
    /// The connector is revoking this Mac with the coordinator.
    case leaving
    /// Done: the panel is back on the pairing screen.
    case left
    case failed(reason: String)

    var title: String {
        switch self {
        case .confirming: return "Leave the neXal network?"
        case .preparing:  return "Preparing to leave the neXal network\u{2026}"
        case .leaving:    return "Leaving the neXal network\u{2026}"
        case .left:       return "Left the neXal network"
        case .failed:     return "Could not leave the neXal network"
        }
    }

    var detail: String {
        switch self {
        case .confirming:
            return "This revokes this Mac, disconnects its tunnel, and removes its local network credentials. You can pair it again afterwards."
        case .preparing:
            return "Finishing the current status check and stopping pairing activity."
        case .leaving:
            return "Revoking this Mac with the coordinator and disconnecting its tunnel."
        case .left:
            return "This Mac is no longer on the neXal network. To pair it again, scan the code below with the neXal iPhone app or enter the manual pairing code."
        case .failed(let reason):
            return reason
        }
    }

    var inProgress: Bool { self == .preparing || self == .leaving }

    var symbol: String {
        switch self {
        case .confirming:           return "questionmark.circle"
        case .preparing, .leaving:  return "arrow.triangle.2.circlepath"
        case .left:                 return "checkmark.circle"
        case .failed:               return "exclamationmark.triangle"
        }
    }

    var severity: IndicatorSeverity {
        switch self {
        case .confirming:           return .warning
        case .preparing, .leaving:  return .pending
        case .left:                 return .good
        case .failed:               return .bad
        }
    }
}
