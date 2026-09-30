import Charts
import SwiftUI

/// Shared layout constants, so spacing and type scale are consistent across the
/// panel instead of being chosen per section (HARDENING-PLAN §26.5).
enum PanelMetrics {
    static let width: CGFloat = 460
    static let height: CGFloat = 700
    static let padding: CGFloat = 20
    static let sectionSpacing: CGFloat = 18
    static let rowSpacing: CGFloat = 10
    static let tightSpacing: CGFloat = 4
    static let chartHeight: CGFloat = 74
    static let symbolWidth: CGFloat = 24
}

/// A titled group of related rows. Status comes first, controls second, setup
/// collapsed; the heading makes that order visible.
struct PanelSection<Content: View>: View {
    let title: String
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: PanelMetrics.tightSpacing + 2) {
            Text(title.uppercased())
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.secondary)
            GroupBox {
                VStack(alignment: .leading, spacing: PanelMetrics.rowSpacing) {
                    content()
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(PanelMetrics.tightSpacing)
            }
        }
    }
}

/// One always-visible indicator row (§26.3). Colour is paired with a distinct
/// symbol and a text label, so the row still reads in grey, in a screenshot and
/// under VoiceOver. The grey tone is drawn as information, never as a warning:
/// no red, no exclamation mark, no badge.
struct IndicatorRow: View {
    let state: IndicatorState
    let tint: Color

    var body: some View {
        HStack(alignment: .top, spacing: PanelMetrics.rowSpacing) {
            Image(systemName: state.systemImage)
                .symbolRenderingMode(.hierarchical)
                .font(.title3)
                .foregroundStyle(state.tone == .colour ? tint : Color.secondary)
                .frame(width: PanelMetrics.symbolWidth, alignment: .center)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(state.heading)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Text(state.label)
                    .font(.subheadline.weight(.medium))
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityLabel(state.accessibilityDescription)
                Text(state.reason)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                if state.detail != nil || !state.detailLines.isEmpty {
                    // A caveat that crowds the row moves into a disclosure; it is
                    // never deleted. Per-subsystem facts are itemised here rather
                    // than collapsed into the one-line summary above (§29.8).
                    DisclosureGroup("What this means") {
                        VStack(alignment: .leading, spacing: PanelMetrics.tightSpacing) {
                            if let detail = state.detail {
                                Text(detail)
                                    .fixedSize(horizontal: false, vertical: true)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            ForEach(state.detailLines, id: \.self) { line in
                                HStack(alignment: .top, spacing: 4) {
                                    Text("•")
                                    Text(line)
                                        .fixedSize(horizontal: false, vertical: true)
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                }
                            }
                        }
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                    }
                    .font(.caption2)
                }
            }
            Spacer(minLength: 0)
        }
    }
}

/// A caveat that would crowd the layout, kept in full inside a disclosure.
struct CaveatDisclosure: View {
    let title: String
    let text: String

    var body: some View {
        DisclosureGroup(title) {
            Text(text)
                .font(.caption2)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
        }
        .font(.caption)
    }
}

/// One small chart with its title, its caption, and an explicit empty state.
/// With no data it says "no data yet" and draws nothing: a flat line at zero
/// would read as a measured value (§26.4).
struct ChartCard<Content: View>: View {
    let title: String
    let caption: String
    let isEmpty: Bool
    let emptyMessage: String
    @ViewBuilder let content: () -> Content

    var body: some View {
        VStack(alignment: .leading, spacing: PanelMetrics.tightSpacing) {
            Text(title)
                .font(.caption.weight(.semibold))
            if isEmpty {
                HStack(alignment: .top, spacing: 6) {
                    Image(systemName: "chart.line.uptrend.xyaxis")
                        .accessibilityHidden(true)
                    Text(emptyMessage)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .font(.caption2)
                .foregroundStyle(.secondary)
                // Deliberately NOT reserving chartHeight here. Four empty charts
                // each holding 74pt of blank space pushed the owner controls off
                // the first screen on first launch, which is when the panel is
                // always empty. The explanation is the content in this state, so
                // the card sizes to it and the panel stays scannable.
                .frame(maxWidth: .infinity, alignment: .topLeading)
            } else {
                content()
                    .frame(height: PanelMetrics.chartHeight)
                Text(caption)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// Activity graph in the style of ScrollableGraphView (as used in the iPhone app):
/// smooth lines over a soft fill, light reference lines, and a plot you drag sideways
/// to scroll back. It opens on the newest samples with half the window visible.
/// Legend labels come from the series name, so it is readable without colour.
struct SeriesChart: View {
    let points: [ConnectorHistory.SeriesPoint]
    let unit: String

    private var upperBound: Double {
        let peak = points.map(\.value).max() ?? 1
        return peak <= 0 ? 1 : peak * 1.15
    }

    private var span: (start: Date, end: Date)? {
        guard let first = points.map(\.at).min(), let last = points.map(\.at).max() else { return nil }
        return (first, last)
    }

    /// Half the recorded range, at least a minute, so there is always something to scroll to.
    private var visibleSeconds: TimeInterval {
        guard let span else { return 60 }
        return max(60, span.end.timeIntervalSince(span.start) / 2)
    }

    var body: some View {
        Chart(points) { point in
            AreaMark(
                x: .value("Time", point.at),
                yStart: .value(unit, 0),
                yEnd: .value(unit, point.value),
                series: .value("Series", point.series)
            )
            .foregroundStyle(by: .value("Series", point.series))
            .opacity(0.18)
            .interpolationMethod(.catmullRom)
            LineMark(
                x: .value("Time", point.at),
                y: .value(unit, point.value),
                series: .value("Series", point.series)
            )
            .foregroundStyle(by: .value("Series", point.series))
            .lineStyle(StrokeStyle(lineWidth: 2, lineCap: .round, lineJoin: .round))
            .interpolationMethod(.catmullRom)
        }
        .chartYScale(domain: 0...upperBound)
        .chartYAxis {
            AxisMarks(position: .leading, values: [0, upperBound / 2, upperBound]) { value in
                AxisGridLine().foregroundStyle(.secondary.opacity(0.25))
                AxisValueLabel {
                    if let v = value.as(Double.self) { Text("\(v, specifier: v < 10 ? "%.1f" : "%.0f") \(unit)").font(.caption2) }
                }
            }
        }
        .chartXAxis {
            AxisMarks(values: .automatic(desiredCount: 4)) { _ in
                AxisValueLabel(format: .dateTime.hour().minute()).font(.caption2)
            }
        }
        .chartScrollableAxes(.horizontal)
        .chartXVisibleDomain(length: visibleSeconds)
        .chartScrollPosition(initialX: (span?.end ?? Date()).addingTimeInterval(-visibleSeconds))
    }
}

/// The pairing code, drawn from the module matrix the Go connector reported.
///
/// It is drawn rather than image-loaded, and it is never written to a file: the
/// modules encode the claim token that authorizes a phone to claim this Mac, so
/// the code exists on screen and nowhere else. A `Canvas` draws it at whatever
/// size the panel gives it, and each module is snapped to a whole point so the
/// rows do not blur into each other at fractional scales — a blurred QR is an
/// unscannable QR, and that failure is invisible until someone tries.
///
/// Rendering is deliberately black-on-white regardless of appearance. An inverted
/// code is a photographic negative, and most scanners refuse one; this is the
/// same reason the CLI has an `--invert` flag for dark terminals rather than
/// guessing.
struct PairingCodeView: View {
    let symbol: PairingSymbol
    /// Dimmed when the pairing can no longer be scanned. The code stays on screen
    /// so the owner can see WHICH code expired, instead of an empty box.
    let isLive: Bool

    var body: some View {
        let padded = symbol.paddedModules
        Canvas { context, size in
            let count = CGFloat(symbol.paddedSize)
            let module = (min(size.width, size.height) / count).rounded(.down)
            guard module >= 1 else { return }
            let side = module * count
            let originX = ((size.width - side) / 2).rounded(.down)
            let originY = ((size.height - side) / 2).rounded(.down)
            context.fill(Path(CGRect(x: originX, y: originY, width: side, height: side)),
                         with: .color(.white))
            for (row, cells) in padded.enumerated() {
                for (column, isDark) in cells.enumerated() where isDark {
                    let rect = CGRect(x: originX + CGFloat(column) * module,
                                      y: originY + CGFloat(row) * module,
                                      width: module, height: module)
                    context.fill(Path(rect), with: .color(.black))
                }
            }
        }
        .frame(width: PairingCodeView.side, height: PairingCodeView.side)
        .background(Color.white)
        .opacity(isLive ? 1 : 0.35)
        .accessibilityLabel(isLive
            ? "Pairing QR code. \(symbol.caption) Scan it with the neXal@home app."
            : "Expired pairing QR code, shown dimmed. \(symbol.caption)")
        // A QR is not meaningfully described by VoiceOver; the surrounding rows
        // carry the role, the status and the countdown as text instead.
        .accessibilityAddTraits(.isImage)
    }

    /// Large enough that a 177-module version-40 symbol still gets one whole point
    /// per module, with the quiet zone included.
    static let side: CGFloat = 200
}
