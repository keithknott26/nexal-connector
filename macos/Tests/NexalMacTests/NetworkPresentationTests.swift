import XCTest
@testable import NexalMac

final class NetworkPresentationTests: XCTestCase {

    private let emptyNetwork = NetworkState(tunnel: .notConfigured, peers: [], thisHostName: "M4 mini")

    // MARK: - Screen routing

    /// The whole point of the redesign: before joining, one thing is offered.
    func testUnlinkedMacShowsOnlyTheJoinScreen() {
        let screen = NetworkScreen.derive(connectorProblem: nil, isLinked: false,
                                         pairing: nil, network: emptyNetwork)
        XCTAssertEqual(screen, .notLinked)
    }

    /// An expired code must not leave a QR on screen that nobody can scan.
    func testExpiredPairingFallsBackToTheJoinScreen() {
        let dead = LinkingState(pairingId: "p1", secondsRemaining: 0, payload: "nexal://pair/p1")
        let screen = NetworkScreen.derive(connectorProblem: nil, isLinked: false,
                                         pairing: dead, network: emptyNetwork)
        XCTAssertEqual(screen, .notLinked, "an expired code should offer a fresh join, not a dead QR")
    }

    func testLivePairingShowsTheLinkingScreen() {
        let live = LinkingState(pairingId: "p1", secondsRemaining: 45, payload: "nexal://pair/p1")
        let screen = NetworkScreen.derive(connectorProblem: nil, isLinked: false,
                                         pairing: live, network: emptyNetwork)
        XCTAssertEqual(screen, .linking(live))
    }

    /// A missing connector outranks everything: no other screen can work without it.
    func testConnectorProblemOutranksPairingAndLinkedState() {
        let live = LinkingState(pairingId: "p1", secondsRemaining: 45, payload: "x")
        let screen = NetworkScreen.derive(connectorProblem: "nexal not found", isLinked: true,
                                         pairing: live, network: emptyNetwork)
        XCTAssertEqual(screen, .needsConnector(reason: "nexal not found"))
    }

    /// An empty problem string is not a problem; it would otherwise wedge the panel.
    func testEmptyProblemStringIsIgnored() {
        let screen = NetworkScreen.derive(connectorProblem: "", isLinked: false,
                                         pairing: nil, network: emptyNetwork)
        XCTAssertEqual(screen, .notLinked)
    }

    func testLinkedMacShowsTheNetworkScreen() {
        let state = NetworkState(tunnel: .quantumSafe(group: "X25519MLKEM768"),
                                 peers: [], thisHostName: "M4 mini")
        let screen = NetworkScreen.derive(connectorProblem: nil, isLinked: true,
                                         pairing: nil, network: state)
        XCTAssertEqual(screen, .linked(state))
    }

    // MARK: - Tunnel indicator

    /// The reported gap: the panel never said whether the tunnel was established.
    func testHybridGroupIsTheOnlyQuantumSafeVerdict() {
        for group in ["X25519MLKEM768", "X25519Kyber768Draft00"] {
            let indicator = TunnelIndicator.derive(configured: true, connected: true, quarantined: false,
                                                   protocolName: "quic", keyAgreement: group)
            XCTAssertEqual(indicator, .quantumSafe(group: group))
            XCTAssertEqual(indicator.severity, .good)
            XCTAssertTrue(indicator.detail.contains(group), "the group should be named, not just asserted")
        }
    }

    /// QUIC alone is not evidence of post-quantum protection, and must not look like it.
    func testQuicWithoutAHybridGroupIsNotReportedAsQuantumSafe() {
        let indicator = TunnelIndicator.derive(configured: true, connected: true, quarantined: false,
                                               protocolName: "quic", keyAgreement: "")
        XCTAssertEqual(indicator, .connectedNotConfirmed(protocolName: "quic"))
        XCTAssertNotEqual(indicator.severity, .good, "an unconfirmed tunnel must not read as success")
        XCTAssertEqual(indicator.severity, .warning)
        XCTAssertTrue(indicator.title.contains("NOT confirmed"))
    }

    /// A classical group is not a hybrid one.
    func testClassicalGroupIsNotAcceptedAsHybrid() {
        let indicator = TunnelIndicator.derive(configured: true, connected: true, quarantined: false,
                                               protocolName: "quic", keyAgreement: "X25519")
        XCTAssertEqual(indicator, .connectedNotConfirmed(protocolName: "quic"))
    }

    /// Quarantine is an error, not a warning: the agent cancels work in this state.
    func testQuarantineOutranksEveryOtherSignal() {
        let indicator = TunnelIndicator.derive(configured: true, connected: true, quarantined: true,
                                               protocolName: "quic", keyAgreement: "X25519MLKEM768")
        XCTAssertEqual(indicator, .quarantined)
        XCTAssertEqual(indicator.severity, .bad)
    }

    func testConfiguredButNotConnectedIsPending() {
        let indicator = TunnelIndicator.derive(configured: true, connected: false, quarantined: false,
                                               protocolName: "", keyAgreement: "")
        XCTAssertEqual(indicator, .connecting)
        XCTAssertEqual(indicator.severity, .pending)
    }

    func testUnconfiguredTunnelSaysSoAndExplainsTheConsequence() {
        let indicator = TunnelIndicator.derive(configured: false, connected: false, quarantined: false,
                                               protocolName: "", keyAgreement: "")
        XCTAssertEqual(indicator, .notConfigured)
        XCTAssertEqual(indicator.severity, .inactive)
        XCTAssertTrue(indicator.detail.lowercased().contains("local network"),
                      "an unconfigured tunnel should say what is still reachable")
    }

    /// Every state must render something; an empty label would be a silent indicator.
    func testEveryIndicatorStateHasTitleAndDetail() {
        let all: [TunnelIndicator] = [.notConfigured, .connecting, .quarantined,
                                      .quantumSafe(group: "X25519MLKEM768"),
                                      .connectedNotConfirmed(protocolName: "quic")]
        for state in all {
            XCTAssertFalse(state.title.isEmpty)
            XCTAssertFalse(state.detail.isEmpty)
        }
    }

    // MARK: - Peers

    func testSameNetworkPeerOffersSshAndVnc() {
        let peer = NetworkPeer(id: "h1", name: "Studio",
                               reachability: .sameNetwork(address: "192.168.1.42", port: 8443))
        XCTAssertEqual(peer.displayAddress, "192.168.1.42")
        XCTAssertEqual(peer.sshCommand(user: "kknott"), "ssh kknott@192.168.1.42")
        XCTAssertTrue(peer.reachability.isConnectable)
    }

    /// The honesty requirement. A remote peer must not yield any address, because the
    /// only candidate is the coordinator's observed NAT address, which cannot be dialed.
    func testRemotePeerOffersNoAddressAtAll() {
        let peer = NetworkPeer(id: "h2", name: "MacBook", reachability: .remoteNoRoute)
        XCTAssertNil(peer.displayAddress)
        XCTAssertNil(peer.sshCommand(user: "kknott"))
        XCTAssertFalse(peer.reachability.isConnectable)
        XCTAssertTrue(peer.statusNote.lowercased().contains("no private route"),
                      "the reason must be stated, not left as a blank field")
    }

    func testPeerWithNoAdvertisedAddressSaysSo() {
        let peer = NetworkPeer(id: "h3", name: "", reachability: .unknown)
        XCTAssertNil(peer.displayAddress)
        XCTAssertFalse(peer.statusNote.isEmpty)
    }

    /// Peers present but none reachable must be explained, not shown as an empty list.
    func testUnreachablePeersProduceAnExplanation() {
        let state = NetworkState(tunnel: .notConfigured,
                                 peers: [NetworkPeer(id: "a", name: "A", reachability: .remoteNoRoute)],
                                 thisHostName: "M4 mini")
        XCTAssertTrue(state.connectablePeers.isEmpty)
        XCTAssertNotNil(state.unreachableNote)
        XCTAssertNil(state.emptyNote, "peers exist, so the empty-list note must not appear")
    }

    func testNoPeersProducesTheEmptyNoteOnly() {
        XCTAssertNil(emptyNetwork.unreachableNote)
        XCTAssertNotNil(emptyNetwork.emptyNote)
    }

    func testReachablePeersSuppressBothNotes() {
        let state = NetworkState(tunnel: .notConfigured,
                                 peers: [NetworkPeer(id: "a", name: "A",
                                                     reachability: .sameNetwork(address: "10.0.0.2", port: 8443))],
                                 thisHostName: "M4 mini")
        XCTAssertNil(state.unreachableNote)
        XCTAssertNil(state.emptyNote)
        XCTAssertEqual(state.connectablePeers.count, 1)
    }
}

final class TunnelEvidenceDecodingTests: XCTestCase {

    private func decode(_ json: String) throws -> TunnelEvidence {
        try JSONDecoder().decode(TunnelEvidence.self, from: Data(json.utf8))
    }

    /// The real shape written by `nexal run`.
    func testDecodesAConfirmedHybridTunnel() throws {
        let evidence = try decode("""
        {"configured":true,"connected":true,"observedProtocol":"quic",
         "observedKeyAgreement":"X25519MLKEM768","verified":false,
         "attestation":false,"quarantined":false,"version":"2026.9.1"}
        """)
        XCTAssertEqual(evidence.indicator, .quantumSafe(group: "X25519MLKEM768"))
    }

    /// `verified` is false in every code path that sets it, so the indicator must not
    /// depend on it -- a tunnel with an observed hybrid group is the strongest evidence
    /// available and must still read as established.
    func testVerifiedFalseDoesNotDowngradeAnObservedHybridGroup() throws {
        let evidence = try decode("""
        {"configured":true,"connected":true,"observedProtocol":"quic",
         "observedKeyAgreement":"X25519MLKEM768","verified":false,"quarantined":false}
        """)
        XCTAssertEqual(evidence.indicator.severity, .good)
    }

    /// Absent fields must default rather than throw: the file does not exist until the
    /// agent has run once, and partial files are possible mid-write.
    func testMissingFieldsDefaultInsteadOfThrowing() throws {
        let evidence = try decode("{}")
        XCTAssertEqual(evidence.indicator, .notConfigured)
    }

    func testUnknownFieldsAreIgnored() throws {
        let evidence = try decode("""
        {"configured":true,"connected":false,"somethingNew":"value"}
        """)
        XCTAssertEqual(evidence.indicator, .connecting)
    }

    func testQuarantineDecodes() throws {
        let evidence = try decode("""
        {"configured":true,"connected":true,"observedProtocol":"quic",
         "observedKeyAgreement":"X25519MLKEM768","quarantined":true}
        """)
        XCTAssertEqual(evidence.indicator, .quarantined)
    }
}

final class PeerRowDecodingTests: XCTestCase {

    private func decode(_ json: String) throws -> PeersView {
        try JSONDecoder().decode(PeersView.self, from: Data(json.utf8))
    }

    func testSameNetworkRowBecomesAConnectablePeer() throws {
        let view = try decode("""
        {"peers":[{"hostId":"h1","name":"Studio","reachability":"same-network",
                   "address":"192.168.1.42","port":8443}],
         "remoteAccessAvailable":false,"directoryStale":false}
        """)
        let peer = view.peers[0].asPeer
        XCTAssertEqual(peer.displayAddress, "192.168.1.42")
        XCTAssertEqual(peer.sshCommand(user: "kknott"), "ssh kknott@192.168.1.42")
    }

    func testNoRouteRowYieldsNoAddress() throws {
        let view = try decode("""
        {"peers":[{"hostId":"h2","name":"MacBook","reachability":"no-route",
                   "note":"Linked, but not on a network this Mac can reach."}]}
        """)
        let peer = view.peers[0].asPeer
        XCTAssertNil(peer.displayAddress)
        XCTAssertNil(peer.sshCommand(user: "kknott"))
    }

    /// A contract violation must not render an empty address line. "same-network" with no
    /// address degrades to unknown rather than producing a blank monospaced row.
    func testSameNetworkWithoutAnAddressDegradesSafely() throws {
        let view = try decode("""
        {"peers":[{"hostId":"h3","reachability":"same-network"}]}
        """)
        let peer = view.peers[0].asPeer
        XCTAssertEqual(peer.reachability, .unknown)
        XCTAssertNil(peer.displayAddress)
    }

    /// An unrecognised value from a newer connector must not be treated as reachable.
    func testUnknownReachabilityValueIsNotConnectable() throws {
        let view = try decode("""
        {"peers":[{"hostId":"h4","reachability":"via-some-future-mesh",
                   "address":"203.0.113.9","port":8443}]}
        """)
        let peer = view.peers[0].asPeer
        XCTAssertFalse(peer.reachability.isConnectable)
        XCTAssertNil(peer.displayAddress, "an unrecognised route must not surface an address")
    }

    func testEmptyPayloadDecodesToAnEmptyView() throws {
        let view = try decode("{}")
        XCTAssertTrue(view.peers.isEmpty)
        XCTAssertFalse(view.remoteAccessAvailable)
    }
}
