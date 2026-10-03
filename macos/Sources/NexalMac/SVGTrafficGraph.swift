import AppKit
import SwiftUI
import WebKit

/// Small SVG line graph of one connection's transfer rate: smooth In and Out lines over
/// gradient fills, light grid, and a legend carrying the current rate and running total.
/// Static between data changes (no looping animation); the lines draw in once when shown.
struct TrafficGraphSpec: Equatable {
    struct Point: Equatable { let t: Double; let v: Double }
    var received: [Point]
    var sent: [Point]
    var receivedTotal: String
    var sentTotal: String

    static let width = 340.0, height = 132.0
    private static let left = 34.0, right = 8.0, top = 40.0, bottom = 14.0
    private static let inHex = "#64D2FF", outHex = "#BF5AF2"

    private static func n(_ d: Double) -> String { String(format: "%.1f", d) }
    private static func rate(_ kbps: Double) -> String {
        kbps >= 1_000 ? String(format: "%.1f MB/s", kbps / 1_000) : String(format: "%.0f KB/s", kbps)
    }
    private static func axis(_ kbps: Double) -> String {
        kbps >= 1_000 ? String(format: "%.1fM", kbps / 1_000) : String(format: "%.0f", kbps)
    }

    /// Smooth path through the points (Catmull-Rom converted to cubic Béziers).
    private static func path(_ pts: [(Double, Double)]) -> String {
        guard let first = pts.first else { return "" }
        var d = "M\(n(first.0)),\(n(first.1))"
        guard pts.count > 1 else { return d }
        for i in 0..<(pts.count - 1) {
            let p0 = pts[max(i - 1, 0)], p1 = pts[i], p2 = pts[i + 1], p3 = pts[min(i + 2, pts.count - 1)]
            let c1 = (p1.0 + (p2.0 - p0.0) / 6, p1.1 + (p2.1 - p0.1) / 6)
            let c2 = (p2.0 - (p3.0 - p1.0) / 6, p2.1 - (p3.1 - p1.1) / 6)
            d += " C\(n(c1.0)),\(n(c1.1)) \(n(c2.0)),\(n(c2.1)) \(n(p2.0)),\(n(p2.1))"
        }
        return d
    }

    var svg: String {
        let w = Self.width, h = Self.height
        let plotW = w - Self.left - Self.right, plotH = h - Self.top - Self.bottom
        let all = received + sent
        let tMin = all.map(\.t).min() ?? 0, tMax = max(all.map(\.t).max() ?? 1, tMin + 1)
        let vMax = max((all.map(\.v).max() ?? 1) * 1.2, 1)
        func xy(_ p: Point) -> (Double, Double) {
            (Self.left + (p.t - tMin) / (tMax - tMin) * plotW, Self.top + plotH - min(p.v / vMax, 1) * plotH)
        }
        var s = "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 \(Self.n(w)) \(Self.n(h))\" width=\"100%\" height=\"100%\">"
        s += """
        <defs>
        <linearGradient id="fi" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="\(Self.inHex)" stop-opacity=".38"/><stop offset="1" stop-color="\(Self.inHex)" stop-opacity="0"/></linearGradient>
        <linearGradient id="fo" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="\(Self.outHex)" stop-opacity=".34"/><stop offset="1" stop-color="\(Self.outHex)" stop-opacity="0"/></linearGradient>
        <filter id="g" x="-10%" y="-30%" width="120%" height="160%"><feGaussianBlur stdDeviation="1.4" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>
        </defs>
        """
        // Legend: arrow, label, current rate, total.
        func legend(_ x: Double, _ hex: String, _ arrow: String, _ label: String, _ now: Double?, _ total: String) -> String {
            """
            <g transform="translate(\(Self.n(x)),4)"><circle cx="5" cy="9" r="4.5" fill="\(hex)"/>
            <text x="14" y="12.5" class="lg">\(arrow) \(label)</text>
            <text x="14" y="26" class="rt">\(now.map(Self.rate) ?? "—")</text>
            <text x="\(Self.n(w / 2 - 24))" y="26" text-anchor="end" class="tot">\(total) total</text></g>
            """
        }
        s += legend(Self.left - 28, Self.inHex, "↓", "Received", received.last?.v, receivedTotal)
        s += legend(w / 2 + 6, Self.outHex, "↑", "Sent", sent.last?.v, sentTotal)
        // Grid + y labels.
        for i in 0...3 {
            let y = Self.top + plotH * Double(i) / 3
            s += "<line x1=\"\(Self.n(Self.left))\" y1=\"\(Self.n(y))\" x2=\"\(Self.n(w - Self.right))\" y2=\"\(Self.n(y))\" class=\"grid\"/>"
            s += "<text x=\"\(Self.n(Self.left - 5))\" y=\"\(Self.n(y + 2.6))\" text-anchor=\"end\" class=\"ax\">\(Self.axis(vMax * Double(3 - i) / 3))</text>"
        }
        if all.count < 2 {
            s += "<text x=\"\(Self.n(w / 2))\" y=\"\(Self.n(Self.top + plotH / 2 + 3))\" text-anchor=\"middle\" class=\"ax\">Collecting data…</text>"
        } else {
            for (pts, hex, fill) in [(received, Self.inHex, "fi"), (sent, Self.outHex, "fo")] where pts.count > 1 {
                let xs = pts.map(xy)
                let line = Self.path(xs)
                let base = Self.top + plotH
                s += "<path d=\"\(line) L\(Self.n(xs.last!.0)),\(Self.n(base)) L\(Self.n(xs.first!.0)),\(Self.n(base)) Z\" fill=\"url(#\(fill))\"/>"
                s += "<path d=\"\(line)\" fill=\"none\" stroke=\"\(hex)\" stroke-width=\"1.8\" stroke-linecap=\"round\" stroke-linejoin=\"round\" filter=\"url(#g)\" pathLength=\"1\" class=\"draw\"/>"
                if let last = xs.last { s += "<circle cx=\"\(Self.n(last.0))\" cy=\"\(Self.n(last.1))\" r=\"2.6\" fill=\"\(hex)\"/>" }
            }
        }
        return s + "</svg>"
    }

    func page() -> String {
        """
        <!doctype html><html><head><meta charset="utf-8"><style>
        :root{--fg:#eaf2ff;--mut:#8fa3c4;--grid:#ffffff14}
        @media (prefers-color-scheme: light){:root{--fg:#0c1b33;--mut:#5a6d8c;--grid:#00000012}}
        html,body{margin:0;height:100%;background:transparent;overflow:hidden;font-family:-apple-system,system-ui,sans-serif}
        .grid{stroke:var(--grid);stroke-width:1} .ax{fill:var(--mut);font-size:7.5px;font-variant-numeric:tabular-nums}
        .lg{fill:var(--mut);font-size:8.5px;font-weight:600;letter-spacing:.04em;text-transform:uppercase}
        .rt{fill:var(--fg);font-size:13px;font-weight:700;font-variant-numeric:tabular-nums} .tot{fill:var(--mut);font-size:8px}
        .draw{stroke-dasharray:1;stroke-dashoffset:0;animation:draw .9s ease-out both} @keyframes draw{from{stroke-dashoffset:1}to{stroke-dashoffset:0}}
        @media (prefers-reduced-motion: reduce){.draw{animation:none}}
        </style></head><body>\(svg)</body></html>
        """
    }
}

struct TrafficGraphView: NSViewRepresentable {
    let spec: TrafficGraphSpec
    func makeCoordinator() -> Coordinator { Coordinator() }
    func makeNSView(context: Context) -> WKWebView {
        let v = PassiveWebView(frame: .zero, configuration: WKWebViewConfiguration())
        v.setValue(false, forKey: "drawsBackground")
        return v
    }
    func updateNSView(_ v: WKWebView, context: Context) {
        guard context.coordinator.last != spec else { return }
        context.coordinator.last = spec
        v.loadHTMLString(spec.page(), baseURL: nil)
    }
    final class Coordinator { var last: TrafficGraphSpec? }
}
