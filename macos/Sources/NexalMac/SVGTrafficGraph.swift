import AppKit
import SwiftUI
import WebKit

/// Small SVG line graph of one connection's transfer rate: smooth In and Out lines over
/// gradient fills, light grid, and a legend carrying the current rate and running total.
/// Static between data changes: no animation, so a refresh never redraws or flickers.
struct TrafficGraphSpec: Equatable {
    struct Point: Equatable { let t: Double; let v: Double }
    var received: [Point]
    var sent: [Point]
    var receivedTotal: String
    var sentTotal: String

    /// Compact: a one-line legend above a short plot.
    static let width = 340.0, height = 92.0
    private static let left = 30.0, right = 6.0, top = 24.0, bottom = 6.0
    private static let inHex = "#64D2FF", outHex = "#BF5AF2"

    private static func n(_ d: Double) -> String { String(format: "%.1f", d) }
    static func rate(_ kbps: Double) -> String {
        if kbps >= 1_000 { return String(format: "%.1f MB/s", kbps / 1_000) }
        if kbps > 0 && kbps < 10 { return String(format: "%.1f KB/s", kbps) }
        return String(format: "%.0f KB/s", kbps)
    }
    private static func axis(_ kbps: Double) -> String {
        if kbps == 0 { return "0" }
        if kbps >= 1_000 { return String(format: kbps >= 10_000 ? "%.0fM" : "%.1fM", kbps / 1_000) }
        return kbps < 10 ? String(format: "%g", kbps) : String(format: "%.0f", kbps)
    }

    /// The axis top: the next "nice" number (1, 2, 5 × 10ⁿ) above the peak, at least 4 KB/s, so
    /// the two tick labels are distinct round values instead of "1, 1, 0, 0".
    static func niceMax(_ peak: Double) -> Double {
        let target = max(peak * 1.1, 4)
        let magnitude = pow(10, floor(log10(target)))
        for step in [1.0, 2.0, 5.0, 10.0] where step * magnitude >= target { return step * magnitude }
        return 10 * magnitude
    }

    /// Smooth path that never overshoots its neighbours (monotone cubic), so an idle line stays on
    /// zero instead of dipping below it after a burst.
    private static func path(_ pts: [(Double, Double)]) -> String {
        guard let first = pts.first else { return "" }
        var d = "M\(n(first.0)),\(n(first.1))"
        guard pts.count > 1 else { return d }
        let count = pts.count
        var slopes = [Double](repeating: 0, count: count - 1)
        for i in 0..<(count - 1) {
            let dx = pts[i + 1].0 - pts[i].0
            slopes[i] = dx == 0 ? 0 : (pts[i + 1].1 - pts[i].1) / dx
        }
        var tangents = [Double](repeating: 0, count: count)
        tangents[0] = slopes[0]; tangents[count - 1] = slopes[count - 2]
        for i in 1..<(count - 1) {
            if slopes[i - 1] * slopes[i] <= 0 { tangents[i] = 0; continue }
            let average = (slopes[i - 1] + slopes[i]) / 2
            // Fritsch–Carlson limit: keeps each segment monotone, so no overshoot.
            let limit = 3 * min(abs(slopes[i - 1]), abs(slopes[i]))
            tangents[i] = average.sign == .minus ? -min(abs(average), limit) : min(abs(average), limit)
        }
        for i in 0..<(count - 1) {
            let (x0, y0) = pts[i], (x1, y1) = pts[i + 1]
            let h = (x1 - x0) / 3
            d += " C\(n(x0 + h)),\(n(y0 + tangents[i] * h)) \(n(x1 - h)),\(n(y1 - tangents[i + 1] * h)) \(n(x1)),\(n(y1))"
        }
        return d
    }

    var svg: String {
        let w = Self.width, h = Self.height
        let plotW = w - Self.left - Self.right, plotH = h - Self.top - Self.bottom
        let all = received + sent
        let tMin = all.map(\.t).min() ?? 0, tMax = max(all.map(\.t).max() ?? 1, tMin + 1)
        let vMax = Self.niceMax(all.map(\.v).max() ?? 0)
        func xy(_ p: Point) -> (Double, Double) {
            (Self.left + (p.t - tMin) / (tMax - tMin) * plotW, Self.top + plotH - min(max(p.v, 0) / vMax, 1) * plotH)
        }
        var s = "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 \(Self.n(w)) \(Self.n(h))\" width=\"100%\" height=\"100%\">"
        s += """
        <defs>
        <linearGradient id="fi" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="\(Self.inHex)" stop-opacity=".30"/><stop offset="1" stop-color="\(Self.inHex)" stop-opacity="0"/></linearGradient>
        <linearGradient id="fo" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="\(Self.outHex)" stop-opacity=".26"/><stop offset="1" stop-color="\(Self.outHex)" stop-opacity="0"/></linearGradient>
        </defs>
        """
        // One-line legend: ● Received 12 KB/s · 151 KB      ● Sent 3 KB/s · 150 KB
        func legend(_ x: Double, _ hex: String, _ label: String, _ now: Double?, _ total: String) -> String {
            """
            <g transform="translate(\(Self.n(x)),2)"><circle cx="4" cy="8" r="3.5" fill="\(hex)"/>
            <text x="12" y="11.5"><tspan class="lg">\(label)</tspan><tspan class="rt" dx="5">\(now.map(Self.rate) ?? "—")</tspan><tspan class="tot" dx="5">\(total)</tspan></text></g>
            """
        }
        s += legend(Self.left - 26, Self.inHex, "Received", received.last?.v, receivedTotal)
        s += legend(w / 2 + 8, Self.outHex, "Sent", sent.last?.v, sentTotal)
        // Two reference lines (top and middle) and a baseline, labelled with round values.
        for (i, frac) in [1.0, 0.5, 0.0].enumerated() {
            let y = Self.top + plotH * (1 - frac)
            s += "<line x1=\"\(Self.n(Self.left))\" y1=\"\(Self.n(y))\" x2=\"\(Self.n(w - Self.right))\" y2=\"\(Self.n(y))\" class=\"\(i == 2 ? "base" : "grid")\"/>"
            s += "<text x=\"\(Self.n(Self.left - 5))\" y=\"\(Self.n(y + 2.6))\" text-anchor=\"end\" class=\"ax\">\(Self.axis(vMax * frac))</text>"
        }
        if all.count < 2 {
            s += "<text x=\"\(Self.n(Self.left + plotW / 2))\" y=\"\(Self.n(Self.top + plotH / 2 + 3))\" text-anchor=\"middle\" class=\"ax\">Collecting data…</text>"
        } else {
            for (pts, hex, fill) in [(received, Self.inHex, "fi"), (sent, Self.outHex, "fo")] where pts.count > 1 {
                let xs = pts.map(xy)
                let line = Self.path(xs)
                let base = Self.top + plotH
                s += "<path d=\"\(line) L\(Self.n(xs.last!.0)),\(Self.n(base)) L\(Self.n(xs.first!.0)),\(Self.n(base)) Z\" fill=\"url(#\(fill))\"/>"
                s += "<path d=\"\(line)\" fill=\"none\" stroke=\"\(hex)\" stroke-width=\"1.6\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>"
                if let last = xs.last { s += "<circle cx=\"\(Self.n(last.0))\" cy=\"\(Self.n(last.1))\" r=\"2.4\" fill=\"\(hex)\"/>" }
            }
        }
        return s + "</svg>"
    }

    func page() -> String {
        """
        <!doctype html><html><head><meta charset="utf-8"><style>
        :root{--fg:#eaf2ff;--mut:#8fa3c4;--grid:#ffffff12;--base:#ffffff26}
        @media (prefers-color-scheme: light){:root{--fg:#0c1b33;--mut:#5a6d8c;--grid:#0000000f;--base:#00000024}}
        html,body{margin:0;height:100%;background:transparent;overflow:hidden;font-family:-apple-system,system-ui,sans-serif}
        .grid{stroke:var(--grid);stroke-width:1;stroke-dasharray:2 3} .base{stroke:var(--base);stroke-width:1}
        .ax{fill:var(--mut);font-size:7px;font-variant-numeric:tabular-nums}
        .lg{fill:var(--mut);font-size:8px;font-weight:600;letter-spacing:.03em;text-transform:uppercase}
        .rt{fill:var(--fg);font-size:10px;font-weight:700;font-variant-numeric:tabular-nums} .tot{fill:var(--mut);font-size:8px}
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
