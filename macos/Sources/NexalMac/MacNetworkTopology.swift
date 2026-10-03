import Foundation

/// Animated SVG map of this Mac, its peers and how each one is reached (LAN, direct over
/// the internet, neXal relay, neXal cloud). Pure and deterministic, so it is testable and the
/// same document can be shown in the panel or saved as a file.
enum TopoKind { case internet, relay, gateway, thisMac, peer, storage }
enum TopoStyle { case lan, wan, direct, relay, cloud, offline }

struct TopoNode: Equatable, Identifiable {
    let id: String
    let kind: TopoKind
    let title: String
    let subtitle: String
    let x: Double
    let y: Double
    let colorHex: String
    let online: Bool
    let tooltip: String
}

struct TopoEdge: Equatable, Identifiable {
    let id: String
    let from: String
    let to: String
    let style: TopoStyle
    let label: String?
    let bend: Double
    let latencyMs: Int?
    let overlay: Bool
}

struct TopoSite: Equatable, Identifiable {
    let id: String
    let title: String
    let x: Double, y: Double, width: Double, height: Double
    let colorHex: String
    let tag: String
}

struct TopoSummary: Equatable {
    var total = 0, online = 0, lan = 0, direct = 0, relayed = 0, offline = 0
    var averageLatency: Int?
}

struct MacNetworkTopology: Equatable {
    static let width = 420.0
    var nodes: [TopoNode] = []
    var edges: [TopoEdge] = []
    var sites: [TopoSite] = []
    var height = 300.0
    var summary = TopoSummary()

    static func color(_ s: TopoStyle) -> String {
        switch s {
        case .lan: return "#34C759"
        case .wan: return "#64D2FF"
        case .direct: return "#32ADE6"
        case .relay: return "#FF9F0A"
        case .cloud: return "#BF5AF2"
        case .offline: return "#8E8E93"
        }
    }

    private static func isOnline(_ p: ConnectorStatus.MeshPeer) -> Bool { p.lifecycle == "connected" }
    private static func isStorage(_ p: ConnectorStatus.MeshPeer) -> Bool { p.name.lowercased().hasPrefix("gw-") }
    private static func isLAN(_ p: ConnectorStatus.MeshPeer) -> Bool { p.path == "direct" && p.directVia == "lan" }

    private static func style(_ p: ConnectorStatus.MeshPeer) -> TopoStyle {
        guard isOnline(p) else { return .offline }
        switch p.path {
        case "direct": return isLAN(p) ? .lan : .direct
        case "relay": return .relay
        case "cloud": return .cloud
        default: return .offline
        }
    }

    private static func routeText(_ p: ConnectorStatus.MeshPeer) -> String {
        guard isOnline(p) else { return "Not connected" }
        switch p.path {
        case "direct": return isLAN(p) ? "Direct on local network" : "Direct over the internet"
        case "relay": return p.relayRegion.map { "Through neXal relay (\($0))" } ?? "Through neXal relay"
        case "cloud": return "Through neXal cloud"
        default: return "Route not reported"
        }
    }

    static func build(hostName: String, peers: [ConnectorStatus.MeshPeer]) -> MacNetworkTopology {
        var t = MacNetworkTopology()
        let w = width, margin = 14.0, cols = 3
        let spacing = (w - margin * 2) / Double(cols)

        let lan = peers.filter { isLAN($0) && isOnline($0) && !isStorage($0) }
        let cloud = peers.filter { isStorage($0) }
        let remote = peers.filter { !isStorage($0) && !(isLAN($0) && isOnline($0)) }
        let usesRelay = peers.contains { isOnline($0) && ($0.path == "relay" || $0.path == "cloud") }

        t.nodes.append(TopoNode(id: "internet", kind: .internet, title: "Internet", subtitle: "Public network",
                                x: usesRelay ? w / 2 - 40 : w / 2, y: 52, colorHex: "#64D2FF", online: true,
                                tooltip: "Public internet"))
        if usesRelay {
            let regions = Set(peers.compactMap { $0.relayRegion }).sorted()
            t.nodes.append(TopoNode(id: "relay", kind: .relay, title: "neXal relay",
                                    subtitle: regions.first ?? "Encrypted fallback", x: w - 62, y: 52,
                                    colorHex: "#FF9F0A", online: true,
                                    tooltip: "Carries traffic only when a direct path is blocked"))
            t.edges.append(TopoEdge(id: "internet-relay", from: "internet", to: "relay", style: .wan, label: nil, bend: 0, latencyMs: nil, overlay: false))
        }

        var cursor = 118.0
        var pending: [(ConnectorStatus.MeshPeer, String)] = []
        var thisID = "this-mac"

        func addSite(id: String, title: String, tag: String, hex: String, members: [ConnectorStatus.MeshPeer], withThis: Bool, home: Bool) {
            let slots = members.count + (withThis ? 1 : 0)
            let rows = max(1, Int((Double(slots) / Double(cols)).rounded(.up)))
            let h = 80.0 + Double(rows) * 100.0
            t.sites.append(TopoSite(id: id, title: title, x: margin - 6, y: cursor, width: w - margin * 2 + 12, height: h, colorHex: hex, tag: tag))
            let gw = "gw-site-" + id
            t.nodes.append(TopoNode(id: gw, kind: .gateway, title: title, subtitle: tag == "LAN" ? "Local network" : "Network",
                                    x: w / 2, y: cursor + 34, colorHex: hex, online: true, tooltip: title))
            t.edges.append(TopoEdge(id: "wan-" + id, from: "internet", to: gw, style: .wan, label: nil, bend: home ? 140 : 0, latencyMs: nil, overlay: false))
            func pos(_ s: Int) -> (Double, Double) {
                let r = s / cols, c = s % cols
                let inRow = min(cols, slots - r * cols)
                let startX = (w - Double(inRow) * spacing) / 2 + spacing / 2
                return (startX + Double(c) * spacing, cursor + 108 + Double(r) * 100.0)
            }
            var slot = 0
            if withThis {
                let (x, y) = pos(slot); slot += 1
                t.nodes.append(TopoNode(id: thisID, kind: .thisMac, title: hostName.isEmpty ? "This Mac" : hostName,
                                        subtitle: "This Mac", x: x, y: y, colorHex: "#0A84FF", online: true, tooltip: "This Mac"))
                t.edges.append(TopoEdge(id: "link-this", from: gw, to: thisID, style: .lan, label: nil, bend: 0, latencyMs: nil, overlay: false))
            }
            for p in members {
                let (x, y) = pos(slot); slot += 1
                let online = isOnline(p), st = style(p)
                let nid = "peer-" + p.id
                let ms = p.latencyMs.map { Int($0.rounded()) }
                let sub = online ? (ms.map { "\($0) ms" } ?? (isStorage(p) ? "storage" : p.path)) : "offline"
                var tip = "\(p.name.isEmpty ? p.id : p.name) — \(routeText(p))"
                if let ms { tip += " — \(ms) ms" }
                if let loss = p.packetLossPercent { tip += String(format: " — %.1f%% loss", loss) }
                tip += p.pq == "protected" ? " — post-quantum protected" : ""
                t.nodes.append(TopoNode(id: nid, kind: isStorage(p) ? .storage : .peer,
                                        title: p.name.isEmpty ? p.id : p.name, subtitle: sub, x: x, y: y,
                                        colorHex: color(st), online: online, tooltip: tip))
                t.edges.append(TopoEdge(id: "spoke-" + nid, from: gw, to: nid,
                                        style: online ? (home ? .lan : .wan) : .offline, label: nil, bend: 0, latencyMs: nil, overlay: false))
                if !home { pending.append((p, nid)) }
                t.summary.total += 1
                if online {
                    t.summary.online += 1
                    switch p.path {
                    case "direct": if isLAN(p) { t.summary.lan += 1 } else { t.summary.direct += 1 }
                    default: t.summary.relayed += 1
                    }
                } else { t.summary.offline += 1 }
            }
            cursor += h + 22
        }

        if !cloud.isEmpty {
            addSite(id: "cloud", title: "neXal cloud", tag: "CLOUD", hex: "#BF5AF2", members: cloud, withThis: false, home: false)
        }
        if !remote.isEmpty {
            addSite(id: "remote", title: "Other networks", tag: "WAN", hex: "#64D2FF", members: remote, withThis: false, home: false)
        }
        thisID = "this-mac"
        addSite(id: "home", title: "Your network", tag: "LAN", hex: "#34C759", members: lan, withThis: true, home: true)

        var idx = 0
        for (p, nid) in pending {
            let online = isOnline(p), st = style(p)
            let side = idx % 2 == 0 ? 1.0 : -1.0
            let bend = side * (44.0 + Double(idx / 2) * 20.0)
            idx += 1
            let ms = p.latencyMs.map { Int($0.rounded()) }
            let label = online ? ms.map { "\($0) ms" } : nil
            if online && (p.path == "relay" || p.path == "cloud") && t.nodes.contains(where: { $0.id == "relay" }) {
                t.edges.append(TopoEdge(id: "mesh-a-" + nid, from: thisID, to: "relay", style: st, label: nil, bend: bend, latencyMs: ms, overlay: true))
                t.edges.append(TopoEdge(id: "mesh-b-" + nid, from: "relay", to: nid, style: st, label: label, bend: -bend, latencyMs: ms, overlay: true))
            } else {
                t.edges.append(TopoEdge(id: "mesh-" + nid, from: thisID, to: nid, style: st, label: label, bend: bend, latencyMs: ms, overlay: true))
            }
        }
        t.height = cursor + 6
        let lat = peers.filter(isOnline).compactMap { $0.latencyMs }
        if !lat.isEmpty { t.summary.averageLatency = Int((lat.reduce(0, +) / Double(lat.count)).rounded()) }
        return t
    }

    // MARK: - SVG

    static func esc(_ s: String) -> String {
        s.replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;").replacingOccurrences(of: "\"", with: "&quot;")
    }
    static func n(_ d: Double) -> String { String(format: "%.1f", d) }
    static func clip(_ s: String, _ n: Int) -> String { s.count > n ? String(s.prefix(n - 1)) + "…" : s }

    private func geometry(_ e: TopoEdge) -> (d: String, mx: Double, my: Double)? {
        guard let a = nodes.first(where: { $0.id == e.from }), let b = nodes.first(where: { $0.id == e.to }) else { return nil }
        let mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2
        let N = Self.n
        if e.bend == 0 { return ("M\(N(a.x)),\(N(a.y)) L\(N(b.x)),\(N(b.y))", mx, my) }
        let cx = mx + e.bend, cy = my
        let lx = 0.25 * a.x + 0.5 * cx + 0.25 * b.x, ly = 0.25 * a.y + 0.5 * cy + 0.25 * b.y
        return ("M\(N(a.x)),\(N(a.y)) Q\(N(cx)),\(N(cy)) \(N(b.x)),\(N(b.y))", lx, ly)
    }

    func svg(animated: Bool = true) -> String {
        let N = Self.n
        var s = "<svg xmlns=\"http://www.w3.org/2000/svg\" xmlns:xlink=\"http://www.w3.org/1999/xlink\" viewBox=\"0 0 \(N(Self.width)) \(N(height))\" width=\"\(N(Self.width))\" height=\"\(N(height))\" font-family=\"-apple-system, system-ui, sans-serif\" role=\"img\" aria-label=\"neXal network map\">"
        s += """
        <defs><style>
        :root{--bg:#070d1a;--bg2:#0e1a30;--fg:#eaf2ff;--mut:#8fa3c4;--grid:#ffffff10;--card:#ffffff0d}
        @media (prefers-color-scheme: light){:root{--bg:#f2f6fc;--bg2:#e3ecf9;--fg:#0c1b33;--mut:#5a6d8c;--grid:#00000010;--card:#ffffffcc}}
        text{fill:var(--fg)} .mut{fill:var(--mut)}
        .flow{stroke-dasharray:6 6;\(animated ? "animation:flow 1.2s linear infinite;" : "")}
        .slow{stroke-dasharray:3 7;\(animated ? "animation:flow 4s linear infinite;" : "")}
        @keyframes flow{to{stroke-dashoffset:-24}}
        .pulse{transform-box:fill-box;transform-origin:center;\(animated ? "animation:pulse 2.6s ease-out infinite;" : "")}
        @keyframes pulse{0%{opacity:.55;transform:scale(1)}100%{opacity:0;transform:scale(1.9)}}
        .float{\(animated ? "animation:float 5s ease-in-out infinite;" : "")}
        @keyframes float{0%,100%{transform:translateY(0)}50%{transform:translateY(-2px)}}
        @media (prefers-reduced-motion: reduce){.flow,.slow,.pulse,.float{animation:none}}
        </style>
        <radialGradient id="bgg" cx="50%" cy="0%" r="90%"><stop offset="0" stop-color="var(--bg2)"/><stop offset="1" stop-color="var(--bg)"/></radialGradient>
        <pattern id="grid" width="26" height="26" patternUnits="userSpaceOnUse"><path d="M26 0H0V26" fill="none" stroke="var(--grid)" stroke-width="1"/></pattern>
        <filter id="glow" x="-50%" y="-50%" width="200%" height="200%"><feGaussianBlur stdDeviation="3" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>
        </defs>
        <rect width="100%" height="100%" fill="url(#bgg)"/><rect width="100%" height="100%" fill="url(#grid)"/>
        """
        for site in sites {
            s += "<rect x=\"\(N(site.x))\" y=\"\(N(site.y))\" width=\"\(N(site.width))\" height=\"\(N(site.height))\" rx=\"22\" fill=\"var(--card)\" stroke=\"\(site.colorHex)\" stroke-opacity=\".55\" stroke-dasharray=\"5 5\"/>"
        }
        let order: [TopoStyle] = [.wan, .lan, .offline, .direct, .relay, .cloud]
        for st in order {
            for e in edges where e.style == st {
                guard let g = geometry(e) else { continue }
                let c = Self.color(e.style)
                let thin = e.id.hasPrefix("spoke-") || e.id.hasPrefix("link-")
                let width = e.overlay ? 2.2 : (thin ? 1.2 : 1.8)
                let opacity = e.style == .offline ? 0.35 : (thin ? 0.45 : 0.9)
                let cls = e.style == .offline ? "" : (e.overlay ? "flow" : "slow")
                s += "<path id=\"\(e.id)\" d=\"\(g.d)\" fill=\"none\" stroke=\"\(c)\" stroke-opacity=\"\(opacity * 0.35)\" stroke-width=\"\(width + 2)\" stroke-linecap=\"round\"/>"
                s += "<path d=\"\(g.d)\" fill=\"none\" stroke=\"\(c)\" stroke-opacity=\"\(opacity)\" stroke-width=\"\(width)\" stroke-linecap=\"round\" class=\"\(cls)\"\(e.style == .offline ? " stroke-dasharray=\"2 6\"" : "")/>"
                if animated && e.style != .offline && !thin {
                    let dur = min(4.5, max(1.2, 1.2 + Double(e.latencyMs ?? 40) / 40))
                    for k in 0..<(e.overlay ? 2 : 1) {
                        s += "<circle r=\"\(e.overlay ? 3.2 : 2.4)\" fill=\"\(c)\" filter=\"url(#glow)\"><animateMotion dur=\"\(N(dur))s\" begin=\"-\(N(dur * Double(k) / 2))s\" repeatCount=\"indefinite\"><mpath xlink:href=\"#\(e.id)\"/></animateMotion></circle>"
                    }
                }
                if let label = e.label, !label.isEmpty {
                    let tw = Double(label.count) * 5.6 + 12
                    s += "<g><rect x=\"\(N(g.mx - tw / 2))\" y=\"\(N(g.my - 9))\" width=\"\(N(tw))\" height=\"17\" rx=\"8.5\" fill=\"var(--bg)\" stroke=\"\(c)\" stroke-opacity=\".8\"/><text x=\"\(N(g.mx))\" y=\"\(N(g.my + 3.5))\" font-size=\"9.5\" font-weight=\"600\" text-anchor=\"middle\">\(Self.esc(label))</text></g>"
                }
            }
        }
        for site in sites {
            s += "<text x=\"\(N(site.x + 14))\" y=\"\(N(site.y + 18))\" font-size=\"9.5\" font-weight=\"700\" letter-spacing=\"1\" class=\"mut\">\(site.tag)</text>"
        }
        for nd in nodes { s += nodeSVG(nd, animated: animated) }
        return s + "</svg>"
    }

    private func nodeSVG(_ nd: TopoNode, animated: Bool) -> String {
        let N = Self.n
        let c = nd.colorHex
        let r = nd.kind == .gateway ? 17.0 : (nd.kind == .internet ? 25.0 : 23.0)
        var g = "<g transform=\"translate(\(N(nd.x)),\(N(nd.y)))\" opacity=\"\(nd.online ? 1.0 : 0.5)\"><title>\(Self.esc(nd.tooltip))</title>"
        if nd.online && animated && nd.kind != .gateway {
            g += "<circle r=\"\(N(r))\" fill=\"none\" stroke=\"\(c)\" stroke-width=\"2\" class=\"pulse\"/>"
        }
        g += "<g class=\"\(nd.kind == .internet || nd.kind == .relay ? "float" : "")\"><circle r=\"\(N(r))\" fill=\"var(--bg)\" stroke=\"\(c)\" stroke-width=\"2\" filter=\"url(#glow)\"/>"
        let st = "fill=\"none\" stroke=\"\(c)\" stroke-width=\"1.8\" stroke-linecap=\"round\" stroke-linejoin=\"round\""
        switch nd.kind {
        case .internet: g += "<path d=\"M-12,6 a6,6 0 0 1 2,-11.5 a9,9 0 0 1 17,1.5 a5.5,5.5 0 0 1 -1,10 z\" \(st)/>"
        case .relay: g += "<path d=\"M0,-12 L10.4,-6 L10.4,6 L0,12 L-10.4,6 L-10.4,-6 Z\" \(st)/><circle r=\"3\" fill=\"\(c)\"/>"
        case .gateway: g += "<rect x=\"-10\" y=\"-2\" width=\"20\" height=\"9\" rx=\"2.5\" \(st)/><path d=\"M-5,-2 L-8,-9 M5,-2 L8,-9\" \(st)/><circle cx=\"-4\" cy=\"2.5\" r=\"1\" fill=\"\(c)\"/><circle cx=\"0\" cy=\"2.5\" r=\"1\" fill=\"\(c)\"/>"
        case .thisMac: g += "<rect x=\"-12\" y=\"-10\" width=\"24\" height=\"15\" rx=\"2.5\" \(st)/><path d=\"M-6,10 H6 M0,5 V10\" \(st)/><circle cx=\"0\" cy=\"-2.5\" r=\"2\" fill=\"\(c)\"/>"
        case .peer: g += "<rect x=\"-12\" y=\"-10\" width=\"24\" height=\"15\" rx=\"2.5\" \(st)/><path d=\"M-6,10 H6 M0,5 V10\" \(st)/>"
        case .storage: g += "<ellipse cx=\"0\" cy=\"-6\" rx=\"9\" ry=\"3.5\" \(st)/><path d=\"M-9,-6 V6 a9,3.5 0 0 0 18,0 V-6\" \(st)/><path d=\"M-9,0 a9,3.5 0 0 0 18,0\" \(st)/>"
        }
        g += "</g>"
        g += "<text y=\"\(N(r + 14))\" font-size=\"11.5\" font-weight=\"650\" text-anchor=\"middle\">\(Self.esc(Self.clip(nd.title, 16)))</text>"
        g += "<text y=\"\(N(r + 27))\" font-size=\"9.5\" text-anchor=\"middle\" class=\"mut\">\(Self.esc(Self.clip(nd.subtitle, 22)))</text></g>"
        return g
    }
}
