import AppKit
import SwiftUI
import WebKit

/// Status banner for the top of the connected panel, drawn as animated SVG: a pulsing status
/// ring with a shield, the headline, and three stat tiles. Display only; every control stays
/// native so keyboard and VoiceOver behave normally.
struct PanelHeroSpec: Equatable {
    enum Tone { case good, notice, idle }
    var tone: Tone
    var title: String
    var subtitle: String
    var stats: [(label: String, value: String)]
    var animated = true

    static func == (a: PanelHeroSpec, b: PanelHeroSpec) -> Bool {
        a.tone == b.tone && a.title == b.title && a.subtitle == b.subtitle && a.animated == b.animated
            && a.stats.count == b.stats.count && zip(a.stats, b.stats).allSatisfy { $0.label == $1.label && $0.value == $1.value }
    }

    private var hex: String {
        switch tone { case .good: return "#34C759"; case .notice: return "#FF9F0A"; case .idle: return "#8E8E93" }
    }
    private static func esc(_ s: String) -> String {
        s.replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "<", with: "&lt;").replacingOccurrences(of: ">", with: "&gt;")
    }
    private static func clip(_ s: String, _ n: Int) -> String { s.count > n ? String(s.prefix(n - 1)) + "…" : s }

    var svg: String {
        let c = hex
        var tiles = ""
        let shown = Array(stats.prefix(3))
        let gap = 8.0, w = (420.0 - gap * 2) / 3
        for (i, st) in shown.enumerated() {
            let x = Double(i) * (w + gap)
            tiles += """
            <g transform="translate(\(String(format: "%.1f", x)),84)"><rect width="\(String(format: "%.1f", w))" height="34" rx="10" fill="var(--card)" stroke="var(--edge)"/>
            <text x="10" y="14" class="k">\(Self.esc(Self.clip(st.label, 18)))</text><text x="10" y="28" class="v" style="font-size:\(String(format: "%.1f", min(12.5, (w - 20) / (Double(max(st.value.count, 1)) * 0.6))))px">\(Self.esc(st.value))</text></g>
            """
        }
        return """
        <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 420 126" width="100%" height="100%">
        <defs>
        <linearGradient id="bg" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="var(--b1)"/><stop offset="1" stop-color="var(--b2)"/></linearGradient>
        <radialGradient id="aura" cx="12%" cy="38%" r="55%"><stop offset="0" stop-color="\(c)" stop-opacity=".28"/><stop offset="1" stop-color="\(c)" stop-opacity="0"/></radialGradient>
        <filter id="glow" x="-50%" y="-50%" width="200%" height="200%"><feGaussianBlur stdDeviation="2.4" result="b"/><feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter>
        </defs>
        <rect x=".5" y=".5" width="419" height="76" rx="16" fill="url(#bg)" stroke="var(--edge)"/>
        <rect x=".5" y=".5" width="419" height="76" rx="16" fill="url(#aura)"/>
        <g transform="translate(40,38)">
          <circle r="22" fill="none" stroke="\(c)" stroke-width="1.5" class="ring r1"/>
          <circle r="22" fill="none" stroke="\(c)" stroke-width="1.5" class="ring r2"/>
          <circle r="21" fill="var(--b1)" stroke="\(c)" stroke-width="2" filter="url(#glow)"/>
          <path d="M0,-11 L9,-7.5 V0.5 C9,6.5 4.5,10.5 0,12.5 C-4.5,10.5 -9,6.5 -9,0.5 V-7.5 Z" fill="none" stroke="\(c)" stroke-width="1.8" stroke-linejoin="round"/>
          <path d="M-4,0.5 L-1,3.5 L4.5,-3" fill="none" stroke="\(c)" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" class="tick"/>
        </g>
        <text x="80" y="35" class="t">\(Self.esc(Self.clip(title, 30)))</text>
        <text x="80" y="54" class="s">\(Self.esc(Self.clip(subtitle, 52)))</text>
        <g>\(tiles)</g>
        </svg>
        """
    }

    func page() -> String {
        """
        <!doctype html><html><head><meta charset="utf-8"><style>
        :root{--b1:#16213a;--b2:#0b1426;--card:#ffffff0a;--edge:#ffffff1f;--fg:#eaf2ff;--mut:#8fa3c4}
        @media (prefers-color-scheme: light){:root{--b1:#ffffff;--b2:#e9f0fb;--card:#ffffffcc;--edge:#0000001c;--fg:#0c1b33;--mut:#5a6d8c}}
        html,body{margin:0;height:100%;background:transparent;overflow:hidden;font-family:-apple-system,system-ui,sans-serif}
        .t{fill:var(--fg);font-size:17px;font-weight:700} .s{fill:var(--mut);font-size:11.5px}
        .k{fill:var(--mut);font-size:8.5px;letter-spacing:.06em;text-transform:uppercase} .v{fill:var(--fg);font-size:12.5px;font-weight:650;font-variant-numeric:tabular-nums}
        .ring{transform-box:fill-box;transform-origin:center;opacity:0;\(animated ? "animation:ping 2.8s ease-out infinite;" : "")} .r2{animation-delay:1.4s}
        @keyframes ping{0%{opacity:.6;transform:scale(1)}100%{opacity:0;transform:scale(1.7)}}
        .tick{stroke-dasharray:20;\(animated ? "animation:draw 1s ease-out both;" : "")} @keyframes draw{from{stroke-dashoffset:20}to{stroke-dashoffset:0}}
        @media (prefers-reduced-motion: reduce){.ring,.tick{animation:none}}
        </style></head><body>\(svg)</body></html>
        """
    }
}

struct PanelHeroView: NSViewRepresentable {
    let spec: PanelHeroSpec
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
    final class Coordinator { var last: PanelHeroSpec? }
}

/// Slim rounded card used to group panel sections in place of bare dividers.
struct PanelCardModifier: ViewModifier {
    func body(content: Content) -> some View {
        content
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(.quaternary.opacity(0.35), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(.separator.opacity(0.6)))
    }
}
extension View { func panelCard() -> some View { modifier(PanelCardModifier()) } }

/// A titled card whose body can be collapsed. The header is a plain button with the state
/// spoken to VoiceOver; the body is simply not built while collapsed.
struct CollapsibleCard<Content: View>: View {
    let title: String
    @State private var expanded: Bool
    let content: () -> Content

    init(title: String, expanded: Bool = true, @ViewBuilder content: @escaping () -> Content) {
        self.title = title
        _expanded = State(initialValue: expanded)
        self.content = content
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Button { withAnimation(.easeInOut(duration: 0.18)) { expanded.toggle() } } label: {
                HStack {
                    Text(title.uppercased()).font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
                    Spacer()
                    Image(systemName: "chevron.right").font(.caption2.weight(.bold)).foregroundStyle(.tertiary)
                        .rotationEffect(.degrees(expanded ? 90 : 0))
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("\(title), \(expanded ? "expanded" : "collapsed")")
            .accessibilityHint("Double-tap to \(expanded ? "collapse" : "expand")")
            if expanded { content() }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.background.opacity(0.35), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(.separator.opacity(0.5)))
    }
}
