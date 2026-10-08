import SwiftUI

/// Speedometer-style dial for one connection's measured download throughput.
///
/// The scale runs from 0 to `maxMbps` (default 100). The colour gradient is
/// red → yellow → green (reversed from latency: higher bandwidth is better).
/// When there is no measurement the dial fades and reads "—".
struct BandwidthGauge: View {
    /// Most recent measured download throughput in megabits per second; nil when unknown.
    let bandwidthMbps: Double?
    /// End of the scale. Anything at or above it pins the needle to the end.
    var maxMbps: Double = 100

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private static let width: CGFloat = 110
    private static let height: CGFloat = 98

    /// The measured value, or nil when missing or not a usable number.
    private var value: Double? {
        guard let bandwidthMbps, bandwidthMbps.isFinite, bandwidthMbps > 0 else { return nil }
        return bandwidthMbps
    }

    /// Position of the needle along the scale, 0...1.
    private var fraction: Double {
        guard let value, maxMbps > 0 else { return 0 }
        return min(max(value / maxMbps, 0), 1)
    }

    var body: some View {
        SVGGaugeView(spec: SVGGaugeSpec(
            fraction: value == nil ? nil : fraction,
            valueText: value.map { Self.formatted($0) } ?? "—",
            unit: value == nil ? "no reading" : "Mbps",
            minLabel: "0", midLabel: Self.formatted(maxMbps / 2), maxLabel: Self.formatted(maxMbps) + "+",
            colors: SVGGaugeSpec.bandwidthColors, animated: !reduceMotion))
            .frame(width: Self.width, height: Self.height)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Bandwidth")
            .accessibilityValue(value.map { Self.formatted($0) + " megabits per second" } ?? "Unknown")
    }

    private static func formatted(_ mbps: Double) -> String {
        if mbps >= 10 {
            return mbps.formatted(.number.precision(.fractionLength(0)))
        }
        return mbps.formatted(.number.precision(.fractionLength(1)))
    }
}
