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

/// Step-drawn series, because every value here is the last reported state rather
/// than a continuously varying signal; interpolating between polls would invent
/// intermediate values. Legend labels come from the series name, so the chart is
/// readable without relying on colour.
struct SeriesChart: View {
    let points: [ConnectorHistory.SeriesPoint]
    let unit: String

    private var upperBound: Double {
        let peak = points.map(\.value).max() ?? 1
        return peak <= 0 ? 1 : peak * 1.15
    }

    var body: some View {
        Chart(points) { point in
            LineMark(
                x: .value("Time", point.at),
                y: .value(unit, point.value)
            )
            .foregroundStyle(by: .value("Series", point.series))
            .interpolationMethod(.stepEnd)
        }
        .chartYScale(domain: 0...upperBound)
        .chartXAxis(.hidden)
    }
}
