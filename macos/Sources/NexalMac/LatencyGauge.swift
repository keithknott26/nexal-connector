import SwiftUI

/// Speedometer-style dial for one connection's round-trip time.
///
/// A 240° arc with coloured zones (green up to 50 ms, yellow to 150 ms, red
/// beyond), a needle at the current value and the value itself printed under the
/// hub. Values past `maxMs` pin the needle to the end of the scale. When there is
/// no measurement the zones fade, the needle is hidden and the value reads "—":
/// nothing is drawn that could be mistaken for a reading.
///
/// Colour is never the only signal: the number is always printed, and VoiceOver
/// reads the gauge as one element, "Latency, 16 milliseconds".
struct LatencyGauge: View {
    /// Current round-trip time in milliseconds; nil when unknown.
    let latencyMs: Double?
    /// End of the scale. Anything at or above it pins the needle to the end.
    var maxMs: Double = 200

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// Degrees of arc the scale covers, symmetric about the top of the dial.
    private static let sweep: Double = 240
    /// Where the scale starts, in SwiftUI's clockwise-from-3-o'clock angles
    /// (150° is lower left, so the arc runs over the top to lower right at 30°).
    private static let startAngle: Double = 150
    private static let width: CGFloat = 110
    private static let height: CGFloat = 98
    private static let dial: CGFloat = 88
    private static let lineWidth: CGFloat = 8

    private static let zones: [(from: Double, to: Double, color: Color)] = [
        (0, 50, .green),
        (50, 150, .yellow),
        (150, .infinity, .red),
    ]

    /// The measured value, or nil when missing or not a usable number.
    private var value: Double? {
        guard let latencyMs, latencyMs.isFinite, latencyMs >= 0 else { return nil }
        return latencyMs
    }

    /// Position of the needle along the scale, 0...1.
    private var fraction: Double {
        guard let value, maxMs > 0 else { return 0 }
        return min(max(value / maxMs, 0), 1)
    }

    var body: some View {
        SVGGaugeView(spec: SVGGaugeSpec(
            fraction: value == nil ? nil : fraction,
            valueText: value.map { Self.formatted($0) } ?? "—",
            unit: value == nil ? "no reading" : "ms",
            minLabel: "0", midLabel: Self.formatted(maxMs / 2), maxLabel: Self.formatted(maxMs) + "+",
            colors: SVGGaugeSpec.latencyColors, animated: !reduceMotion))
            .frame(width: Self.width, height: Self.height)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Latency")
            .accessibilityValue(value.map { Self.formatted($0) + " milliseconds" } ?? "Unknown")
    }

    /// Coloured zones, needle and hub, drawn in a square the size of the dial.
    private var dialFace: some View {
        ZStack {
            ForEach(Array(Self.zones.enumerated()), id: \.offset) { _, zone in
                Circle()
                    .trim(from: arcFraction(zone.from), to: arcFraction(zone.to))
                    .stroke(zone.color.opacity(value == nil ? 0.25 : 0.85),
                            style: StrokeStyle(lineWidth: Self.lineWidth, lineCap: .butt))
                    .rotationEffect(.degrees(Self.startAngle))
                    .padding(Self.lineWidth / 2)
            }
            if value != nil {
                // Drawn pointing straight up from the centre, then turned; the
                // offset moves only the drawing, so the turn is about the hub.
                Capsule()
                    .fill(Color.primary)
                    .frame(width: 2.5, height: Self.dial / 2 - Self.lineWidth - 4)
                    .offset(y: -(Self.dial / 2 - Self.lineWidth - 4) / 2)
                    .rotationEffect(.degrees(Self.sweep * fraction - Self.sweep / 2))
                    .animation(reduceMotion ? nil : .easeOut(duration: 0.6), value: fraction)
            }
            Circle()
                .fill(Color.primary)
                .frame(width: 7, height: 7)
        }
    }

    /// A value in milliseconds as a trim position on a full circle, clamped to the scale.
    private func arcFraction(_ ms: Double) -> CGFloat {
        let clamped = min(max(ms / maxMs, 0), 1)
        return CGFloat(clamped * Self.sweep / 360)
    }

    private func scaleLabel(_ text: String) -> some View {
        Text(text)
            .font(.system(size: 8).monospacedDigit())
            .foregroundStyle(.secondary)
    }

    private static func formatted(_ ms: Double) -> String {
        ms.formatted(.number.precision(.fractionLength(0)))
    }
}
