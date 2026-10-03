import AppKit
import SwiftUI
import WebKit

/// A slick animated SVG speedometer: 270° gradient track, tick marks, glowing value arc,
/// tapered needle on a machined hub and a large readout. Drawn as SVG and animated with CSS
/// transitions, so a new reading sweeps smoothly from the old one instead of jumping.
struct SVGGaugeSpec: Equatable {
    /// Needle position along the scale, 0...1. nil draws an idle dial with no needle.
    var fraction: Double?
    var valueText: String
    var unit: String
    /// Scale labels at the start, middle and end of the arc.
    var minLabel = "0"
    var midLabel = ""
    var maxLabel = ""
    /// Track gradient, start to end. Latency: green → amber → red. Traffic: cyan → blue → violet.
    var colors: [String] = ["#34C759", "#FFD60A", "#FF453A"]
    var animated = true

    static let latencyColors = ["#34C759", "#FFD60A", "#FF453A"]
    static let trafficColors = ["#64D2FF", "#0A84FF", "#BF5AF2"]

    // Geometry: centre (60,60), radius 44, sweep 270° with the gap at the bottom.
    private static let cx = 60.0, cy = 60.0, r = 44.0
    static let sweep = 270.0, start = 135.0

    private static func point(_ deg: Double, _ radius: Double) -> (Double, Double) {
        let a = deg * .pi / 180
        return (cx + radius * cos(a), cy + radius * sin(a))
    }
    private static func n(_ d: Double) -> String { String(format: "%.2f", d) }
    private static func esc(_ s: String) -> String {
        s.replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;")
    }

    var clamped: Double { min(max(fraction ?? 0, 0), 1) }
    var needleDegrees: Double { -Self.sweep / 2 + Self.sweep * clamped }

    var svg: String {
        let s = Self.point(Self.start, Self.r), e = Self.point(Self.start + Self.sweep, Self.r)
        let arc = "M\(Self.n(s.0)),\(Self.n(s.1)) A\(Self.n(Self.r)),\(Self.n(Self.r)) 0 1 1 \(Self.n(e.0)),\(Self.n(e.1))"
        let stops = colors.enumerated().map { i, c in
            "<stop offset=\"\(Self.n(Double(i) / Double(max(colors.count - 1, 1))))\" stop-color=\"\(c)\"/>"
        }.joined()
        var ticks = ""
        for i in 0...40 {
            let major = i % 5 == 0
            let deg = Self.start + Self.sweep * Double(i) / 40
            let a = Self.point(deg, Self.r + 4.2), b = Self.point(deg, Self.r + (major ? 8.2 : 6.2))
            ticks += "<line x1=\"\(Self.n(a.0))\" y1=\"\(Self.n(a.1))\" x2=\"\(Self.n(b.0))\" y2=\"\(Self.n(b.1))\" stroke-width=\"\(major ? 1.1 : 0.6)\" class=\"tick\"/>"
        }
        let idle = fraction == nil
        let l0 = Self.point(Self.start, Self.r + 1), l2 = Self.point(Self.start + Self.sweep, Self.r + 1)
        return """
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 108" width="100%" height="100%" role="img">
        <defs>
        <linearGradient id="tg" gradientUnits="userSpaceOnUse" x1="16" y1="0" x2="104" y2="0">\(stops)</linearGradient>
        <radialGradient id="face" cx="50%" cy="42%" r="62%"><stop offset="0" stop-color="var(--face1)"/><stop offset="1" stop-color="var(--face2)"/></radialGradient>
        <radialGradient id="hub" cx="35%" cy="30%" r="80%"><stop offset="0" stop-color="#f4f7ff"/><stop offset="1" stop-color="#7d8aa6"/></radialGradient>
        <filter id="glow" x="-30%" y="-30%" width="160%" height="160%"><feGaussianBlur stdDeviation="1.6" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>
        <filter id="shadow" x="-30%" y="-30%" width="160%" height="160%"><feDropShadow dx="0" dy="0.8" stdDeviation="0.9" flood-color="#000" flood-opacity=".55"/></filter>
        </defs>
        <circle cx="60" cy="60" r="55" fill="url(#face)" stroke="var(--rim)" stroke-width="1.2"/>
        <circle cx="60" cy="60" r="52.5" fill="none" stroke="var(--rim2)" stroke-width=".6"/>
        <path d="\(arc)" fill="none" stroke="var(--track)" stroke-width="6.5" stroke-linecap="round"/>
        <path id="zones" d="\(arc)" fill="none" stroke="url(#tg)" stroke-width="6.5" stroke-linecap="round" opacity="\(idle ? 0.18 : 0.30)"/>
        <path id="val" d="\(arc)" pathLength="100" fill="none" stroke="url(#tg)" stroke-width="6.5" stroke-linecap="round" filter="url(#glow)"
              stroke-dasharray="100" stroke-dashoffset="\(Self.n(100 * (1 - clamped)))" opacity="\(idle ? 0 : 1)"/>
        \(ticks)
        <text x="\(Self.n(l0.0 - 1))" y="\(Self.n(l0.1 + 13))" text-anchor="middle" class="lab">\(Self.esc(minLabel))</text>
        <text x="60" y="\(Self.n(Self.cy - Self.r - 12.5))" text-anchor="middle" class="lab">\(Self.esc(midLabel))</text>
        <text x="\(Self.n(l2.0 + 1))" y="\(Self.n(l2.1 + 13))" text-anchor="middle" class="lab">\(Self.esc(maxLabel))</text>
        <g id="needle" transform="rotate(\(Self.n(needleDegrees)) 60 60)" opacity="\(idle ? 0 : 1)" filter="url(#shadow)">
        <path d="M60,19 L62.6,62 L57.4,62 Z" fill="var(--needle)"/>
        <path d="M60,19 L60.9,62 L60,62 Z" fill="#fff" opacity=".35"/>
        <circle cx="60" cy="19" r="1.1" fill="#FF453A"/>
        </g>
        <circle cx="60" cy="60" r="6.4" fill="url(#hub)" stroke="var(--rim)" stroke-width=".8"/>
        <circle cx="60" cy="60" r="2.2" fill="var(--hubdot)"/>
        <text id="readout" x="60" y="86" text-anchor="middle" class="read">\(Self.esc(valueText))</text>
        <text id="unit" x="60" y="96.5" text-anchor="middle" class="unit">\(Self.esc(unit))</text>
        </svg>
        """
    }

    func page() -> String {
        """
        <!doctype html><html><head><meta charset="utf-8"><style>
        :root{--face1:#1b2540;--face2:#0a1020;--rim:#3a4a70;--rim2:#ffffff14;--track:#ffffff12;--tick:#8fa3c4;--needle:#eef3ff;--hubdot:#0a1020;--fg:#eaf2ff;--mut:#8fa3c4}
        @media (prefers-color-scheme: light){:root{--face1:#ffffff;--face2:#e6edf8;--rim:#b4c2dc;--rim2:#00000010;--track:#0000000d;--tick:#5a6d8c;--needle:#16233f;--hubdot:#e6edf8;--fg:#0c1b33;--mut:#5a6d8c}}
        html,body{margin:0;height:100%;background:transparent;overflow:hidden;font-family:-apple-system,system-ui,sans-serif}
        .tick{stroke:var(--tick)} .lab{fill:var(--mut);font-size:5.6px;font-variant-numeric:tabular-nums}
        .read{fill:var(--fg);font-size:15px;font-weight:700;font-variant-numeric:tabular-nums}
        .unit{fill:var(--mut);font-size:6.4px;letter-spacing:.08em;text-transform:uppercase}
        #needle{transition:transform .6s cubic-bezier(.2,.8,.2,1),opacity .3s}
        #val{transition:stroke-dashoffset .6s cubic-bezier(.2,.9,.25,1),opacity .3s}
        @media (prefers-reduced-motion: reduce){#needle,#val{transition:none}}
        </style></head><body>\(svg)
        <script>
        function setGauge(f,deg,text,unit,idle){
          var n=document.getElementById('needle'),v=document.getElementById('val');
          n.setAttribute('transform','rotate('+deg+' 60 60)');n.style.opacity=idle?0:1;
          v.style.strokeDashoffset=100*(1-f);v.style.opacity=idle?0:1;
          document.getElementById('zones').setAttribute('opacity',idle?0.18:0.30);
          document.getElementById('readout').textContent=text;document.getElementById('unit').textContent=unit;}
        </script></body></html>
        """
    }

    /// JavaScript that moves an already-loaded gauge to this reading.
    var updateScript: String {
        let text = valueText.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "'", with: "\\'")
        let u = unit.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "'", with: "\\'")
        return "setGauge(\(clamped),\(needleDegrees),'\(text)','\(u)',\(fraction == nil ? "true" : "false"));"
    }
}

/// Inert web view: it never takes the mouse or scroll wheel, so a gauge inside the panel's
/// scroll view cannot stop the panel scrolling.
final class PassiveWebView: WKWebView {
    override func scrollWheel(with event: NSEvent) { nextResponder?.scrollWheel(with: event) }
    override func hitTest(_ point: NSPoint) -> NSView? { nil }
}

struct SVGGaugeView: NSViewRepresentable {
    let spec: SVGGaugeSpec

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeNSView(context: Context) -> WKWebView {
        let view = PassiveWebView(frame: .zero, configuration: WKWebViewConfiguration())
        view.setValue(false, forKey: "drawsBackground")
        view.navigationDelegate = context.coordinator
        context.coordinator.loadedSpec = spec
        context.coordinator.initialSpec = spec
        view.loadHTMLString(spec.page(), baseURL: nil)
        return view
    }

    func updateNSView(_ view: WKWebView, context: Context) {
        let c = context.coordinator
        guard c.loadedSpec != spec else { return }
        c.loadedSpec = spec
        if c.ready { view.evaluateJavaScript(spec.updateScript, completionHandler: nil) }
    }

    final class Coordinator: NSObject, WKNavigationDelegate {
        var loadedSpec: SVGGaugeSpec?
        var initialSpec: SVGGaugeSpec?
        var ready = false
        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            ready = true
            // A reading that arrived while the page was loading.
            if let spec = loadedSpec, spec != initialSpec { webView.evaluateJavaScript(spec.updateScript, completionHandler: nil) }
        }
    }
}
