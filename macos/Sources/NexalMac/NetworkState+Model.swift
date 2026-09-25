import AppKit
import Foundation

/// Decoded `tunnel-evidence.json`, which `nexal run` writes beside config.json.
///
/// The panel never reported whether the post-quantum tunnel was up. The data existed the
/// whole time -- the agent writes this file on every observation, and `status` carries a
/// `pq` object -- but nothing in the app decoded either, so the one fact most worth
/// showing was the one the UI omitted.
///
/// Every field is optional and defaulted: this file is absent until the agent has run
/// once, and a missing file must read as "not configured" rather than crash the panel.
struct TunnelEvidence: Decodable, Equatable {
    var configured: Bool = false
    var connected: Bool = false
    var observedProtocol: String = ""
    var observedKeyAgreement: String = ""
    var quarantined: Bool = false

    enum CodingKeys: String, CodingKey {
        case configured, connected, observedProtocol, observedKeyAgreement, quarantined
    }

    init() {}

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        configured = (try? c.decode(Bool.self, forKey: .configured)) ?? false
        connected = (try? c.decode(Bool.self, forKey: .connected)) ?? false
        observedProtocol = (try? c.decode(String.self, forKey: .observedProtocol)) ?? ""
        observedKeyAgreement = (try? c.decode(String.self, forKey: .observedKeyAgreement)) ?? ""
        quarantined = (try? c.decode(Bool.self, forKey: .quarantined)) ?? false
    }

    var indicator: TunnelIndicator {
        TunnelIndicator.derive(configured: configured, connected: connected,
                               quarantined: quarantined,
                               protocolName: observedProtocol,
                               keyAgreement: observedKeyAgreement)
    }
}

/// One row of `nexal peers-view`, mirroring agent.PeerRow.
///
/// `reachability` is decided by the connector, not here. The UI must not compute it: the
/// only address available for an off-network peer is the coordinator's observed NAT
/// address, which is the peer's router rather than the peer, and it would look like a
/// working ssh target while being unusable.
struct PeerRowDTO: Decodable, Equatable {
    let hostId: String
    var name: String?
    var reachability: String
    var address: String?
    var port: UInt16?
    var note: String?

    var asPeer: NetworkPeer {
        let route: PeerReachability
        switch reachability {
        case "same-network":
            if let address, !address.isEmpty {
                route = .sameNetwork(address: address, port: port ?? 0)
            } else {
                // Defensive: "same-network" with no address is a contract violation.
                // Treating it as reachable would render an empty monospaced line where
                // an address belongs, so it degrades to the honest case.
                route = .unknown
            }
        case "no-route":
            route = .remoteNoRoute
        default:
            route = .unknown
        }
        return NetworkPeer(id: hostId, name: name ?? "", reachability: route)
    }
}

struct PeersView: Decodable, Equatable {
    var peers: [PeerRowDTO] = []
    var remoteAccessAvailable: Bool = false
    var directoryStale: Bool = false

	private enum CodingKeys: String, CodingKey { case peers, remoteAccessAvailable, directoryStale }
	init() {}
	init(from decoder: Decoder) throws {
		let c = try decoder.container(keyedBy: CodingKeys.self)
		peers = (try? c.decode([PeerRowDTO].self, forKey: .peers)) ?? []
		remoteAccessAvailable = (try? c.decode(Bool.self, forKey: .remoteAccessAvailable)) ?? false
		directoryStale = (try? c.decode(Bool.self, forKey: .directoryStale)) ?? false
	}
}

extension AppModel {
    /// What the menu bar shows, derived rather than stored, so it cannot drift from the
    /// underlying status.
    var networkScreen: NetworkScreen {
        NetworkScreen.derive(connectorProblem: connectorProblem,
                             isLinked: isLinked,
                             pairing: linkingState,
                             network: networkState)
    }

    /// Linked means this Mac has a host identity from the coordinator. A running
    /// connector that has never enrolled is not linked, which is exactly the state the
    /// join button exists for.
    ///
    /// When the agent is not answering (stopped, restarting after an update)
    /// there is no status, but the saved host identity still says this Mac is
    /// paired. Offering a new pairing code then would wrongly look like the Mac
    /// lost its association, so the saved identity counts as linked.
    var isLinked: Bool {
        if let hostId = status?.hostId { return !hostId.isEmpty }
        return hasPersistedHostIdentity
    }

    var networkState: NetworkState {
        NetworkState(tunnel: tunnelEvidence.indicator,
                     peers: peersView.peers.map(\.asPeer),
                     thisHostName: hostNameDisplay)
    }

    var linkingState: LinkingState? {
        guard let pairing else { return nil }
        // secondsRemaining is optional: nil means the connector gave no expiry. Treated
        // as expired rather than unlimited, so a code with an unknown lifetime is
        // replaced instead of being shown indefinitely as though it still worked.
        return LinkingState(pairingId: pairing.pairingId,
                            secondsRemaining: max(0, pairing.secondsRemaining(now: pairingTick) ?? 0),
                            payload: pairing.pairingId)
    }

    var hostNameDisplay: String {
        if let name = status?.name, !name.isEmpty { return name }
        return hostName
    }

    /// The account name for a copyable ssh command. NSUserName is the local user, which
    /// is a guess about the REMOTE Mac -- correct for the common case of one person's
    /// machines, and the command is offered for copying rather than executed, so it can
    /// be edited when it is wrong.
    var loginName: String { NSUserName() }

    /// Non-nil when the panel cannot proceed at all.
    var connectorProblem: String? {
        if selection == nil { return "Choose the nexal connector to continue." }
        if !configurationExists && status == nil { return message }
        return nil
    }
}

extension AppModel {
    /// The menu bar glyph. This is the only indicator visible without opening the panel,
    /// so it reports the tunnel rather than the contribution flag -- and a tunnel that is
    /// merely connected does not get the same symbol as one confirmed post-quantum.
    var menuBarSymbol: String {
        if leavePhase?.inProgress == true { return "arrow.triangle.2.circlepath" }
        guard isLinked else { return "circle.dashed" }
        switch tunnelEvidence.indicator {
        case .quantumSafe:           return "lock.shield.fill"
        case .connectedNotConfirmed: return "exclamationmark.shield"
        case .quarantined:           return "xmark.shield.fill"
        case .connecting:            return "shield.lefthalf.filled"
        case .notConfigured:         return "shield"
        }
    }
}
