import AppKit
import SwiftUI
import UniformTypeIdentifiers
import WebKit

/// The animated network map inside the connector panel. Rendered by WebKit so the SVG's own
/// animation runs unchanged; the same document can be saved from the button.
/// A button in the panel; the map itself opens in a sheet sized to the SVG so the web view
/// never sits inside (and captures scrolling from) the panel's own scroll view.
struct MacNetworkTopologyView: View {
    let hostName: String
    let peers: [ConnectorStatus.MeshPeer]
    @State private var showing = false

    var body: some View {
        Button { showing = true } label: {
            Label("View Network Topology", systemImage: "point.3.connected.trianglepath.dotted")
        }
        .accessibilityIdentifier("network-map")
        .sheet(isPresented: $showing) { MacNetworkTopologySheet(hostName: hostName, peers: peers) }
    }
}

private struct MacNetworkTopologySheet: View {
    let hostName: String
    let peers: [ConnectorStatus.MeshPeer]
    @State private var animated = true
    @Environment(\.dismiss) private var dismiss
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let topo = MacNetworkTopology.build(hostName: hostName, peers: peers)
        let play = animated && !reduceMotion
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Network topology").font(.headline)
                Spacer()
                if !reduceMotion {
                    Button { animated.toggle() } label: { Image(systemName: animated ? "pause.circle" : "play.circle") }
                        .buttonStyle(.borderless).help(animated ? "Pause animation" : "Play animation")
                        .accessibilityLabel(animated ? "Pause animation" : "Play animation")
                }
                Button { save(topo) } label: { Image(systemName: "square.and.arrow.up") }
                    .buttonStyle(.borderless).help("Save the map as an SVG file").accessibilityLabel("Save map as SVG")
                Button("Done") { dismiss() }.keyboardShortcut(.defaultAction)
            }
            summary(topo.summary)
            ScrollView(.vertical, showsIndicators: true) {
                TopologyWebView(svg: topo.svg(animated: play))
                    .frame(width: MacNetworkTopology.width, height: topo.height)
                    .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
                    .overlay(RoundRectangleBorder())
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel("Network map. \(topo.summary.online) of \(topo.summary.total) computers online, \(topo.summary.lan) on the local network, \(topo.summary.direct) direct over the internet, \(topo.summary.relayed) relayed, \(topo.summary.offline) offline.")
            }
        }
        .padding(20)
        .frame(width: MacNetworkTopology.width + 40, height: min(topo.height + 110, 780))
    }

    private struct RoundRectangleBorder: View {
        var body: some View { RoundedRectangle(cornerRadius: 16, style: .continuous).strokeBorder(.white.opacity(0.08)) }
    }

    private func summary(_ s: TopoSummary) -> some View {
        HStack(spacing: 6) {
            chip("\(s.online)/\(s.total) online", .green)
            if s.lan > 0 { chip("\(s.lan) LAN", .green) }
            if s.direct > 0 { chip("\(s.direct) direct", .cyan) }
            if s.relayed > 0 { chip("\(s.relayed) relayed", .orange) }
            if let ms = s.averageLatency { chip("\(ms) ms avg", .blue) }
            if s.offline > 0 { chip("\(s.offline) offline", .gray) }
        }
    }

    private func chip(_ text: String, _ tint: Color) -> some View {
        Text(text).font(.caption2.weight(.semibold)).foregroundStyle(tint)
            .padding(.horizontal, 8).padding(.vertical, 3).background(tint.opacity(0.14), in: Capsule())
    }

    private func save(_ topo: MacNetworkTopology) {
        let panel = NSSavePanel()
        panel.nameFieldStringValue = "nexal-network-map.svg"
        panel.allowedContentTypes = [.svg]
        guard panel.runModal() == .OK, let url = panel.url else { return }
        try? topo.svg(animated: true).write(to: url, atomically: true, encoding: .utf8)
    }
}

/// A WKWebView keeps scroll-wheel events for itself; the map has nothing to scroll, so hand them
/// to the enclosing SwiftUI ScrollView instead of trapping the pointer.
private final class PassthroughWebView: WKWebView {
    override func scrollWheel(with event: NSEvent) { nextResponder?.scrollWheel(with: event) }
}

private struct TopologyWebView: NSViewRepresentable {
    let svg: String

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeNSView(context: Context) -> WKWebView {
        let view = PassthroughWebView(frame: .zero, configuration: WKWebViewConfiguration())
        view.setValue(false, forKey: "drawsBackground")
        return view
    }

    func updateNSView(_ view: WKWebView, context: Context) {
        guard context.coordinator.last != svg else { return }
        context.coordinator.last = svg
        let html = "<!doctype html><html><head><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><style>html,body{margin:0;overflow:hidden;background:transparent}svg{display:block;width:100%;height:auto}</style></head><body>\(svg)</body></html>"
        view.loadHTMLString(html, baseURL: nil)
    }

    final class Coordinator { var last = "" }
}
