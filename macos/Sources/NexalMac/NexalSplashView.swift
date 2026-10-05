import SwiftUI

/// The neXal launch animation, played once.
///
/// A cloud draws, its peers appear and mesh together, and the progress bar
/// fills. When the bar completes the topology dims and the lock appears; the
/// lock is the last thing on screen. The host decides when to dismiss it,
/// normally after `NexalSplashView.duration`.
///
/// SwiftUI only, no platform imports: the same file lives in nexal-ios and
/// nexal-connector. Change both copies together.
struct NexalSplashView: View {
    /// Seconds from first frame to the end of the hold on the lock.
    static let duration: TimeInterval = 4.3

    /// false shows the finished frame (the lock) without animating: the cover used while the app is in the app switcher.
    var animated = true
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var start = Date()

    var body: some View {
        TimelineView(.animation(minimumInterval: nil, paused: reduceMotion || !animated)) { timeline in
            let t = (reduceMotion || !animated) ? Self.duration : timeline.date.timeIntervalSince(start)
            Canvas { context, size in
                var ctx = context
                Self.draw(at: t, in: &ctx, size: size)
            }
        }
        .background(Color.black)
        .accessibilityElement()
        .accessibilityLabel("neXal systems. Quantum-safe, NIST level 5 encryption.")
    }

    // MARK: Timeline (seconds)

    private enum Beat {
        static let cloud = 0.0...0.8
        static let peers = 0.6...1.1
        static let links = 1.0...1.8
        static let bar = 0.0...3.2
        static let dim = 3.2...3.45
        static let lock = 3.25...3.5
    }

    /// 0...1 progress through `range` at time `t`, eased in and out.
    private static func progress(_ t: Double, _ range: ClosedRange<Double>) -> Double {
        let x = min(max((t - range.lowerBound) / (range.upperBound - range.lowerBound), 0), 1)
        return x * x * (3 - 2 * x)
    }

    // MARK: Drawing
    //
    // Drawn in a 180 x 340 design space, scaled to fit and centred.

    private static let cloudBase = CGPoint(x: 90, y: 112)
    private static let peers: [CGPoint] = [CGPoint(x: 30, y: 150), CGPoint(x: 60, y: 180),
                                           CGPoint(x: 120, y: 180), CGPoint(x: 150, y: 150)]
    private static let links: [(CGPoint, CGPoint)] = [
        (CGPoint(x: 37, y: 150), CGPoint(x: 67, y: 180)),
        (CGPoint(x: 67, y: 180), CGPoint(x: 113, y: 180)),
        (CGPoint(x: 113, y: 180), CGPoint(x: 143, y: 150)),
        (CGPoint(x: 37, y: 150), CGPoint(x: 143, y: 150)),
        (CGPoint(x: 37, y: 150), CGPoint(x: 113, y: 180)),
        (CGPoint(x: 67, y: 180), CGPoint(x: 143, y: 150)),
        (cloudBase, CGPoint(x: 37, y: 143)),
        (cloudBase, CGPoint(x: 143, y: 143))
    ]

    private static func draw(at t: Double, in ctx: inout GraphicsContext, size: CGSize) {
        let scale = min(size.width / 180, size.height / 340)
        ctx.translateBy(x: (size.width - 180 * scale) / 2, y: (size.height - 340 * scale) / 2)
        ctx.scaleBy(x: scale, y: scale)

        let white = GraphicsContext.Shading.color(.white)
        let topologyOpacity = 1 - 0.88 * progress(t, Beat.dim)

        ctx.drawLayer { layer in
            layer.opacity = topologyOpacity
            layer.stroke(cloudPath.trimmedPath(from: 0, to: progress(t, Beat.cloud)),
                         with: white, style: StrokeStyle(lineWidth: 1.5, lineCap: .round, lineJoin: .round))
            let linkProgress = progress(t, Beat.links)
            if linkProgress > 0 {
                for (a, b) in links {
                    var line = Path()
                    line.move(to: a)
                    line.addLine(to: b)
                    layer.stroke(line.trimmedPath(from: 0, to: linkProgress), with: white, lineWidth: 0.8)
                }
            }
            layer.opacity = topologyOpacity * progress(t, Beat.peers)
            for p in peers {
                let box = Path(roundedRect: CGRect(x: p.x - 6, y: p.y - 6, width: 12, height: 12), cornerRadius: 3)
                layer.fill(box, with: .color(.black))
                layer.stroke(box, with: white, lineWidth: 1.5)
                layer.fill(Path(ellipseIn: CGRect(x: p.x - 1.6, y: p.y - 1.6, width: 3.2, height: 3.2)), with: white)
            }
        }

        let lockProgress = progress(t, Beat.lock)
        if lockProgress > 0 {
            ctx.drawLayer { layer in
                layer.opacity = lockProgress
                let pop = 0.85 + 0.15 * lockProgress
                layer.translateBy(x: 90, y: 143)
                layer.scaleBy(x: pop, y: pop)
                layer.translateBy(x: -90, y: -143)
                let body = Path(roundedRect: CGRect(x: 78, y: 134, width: 24, height: 18), cornerRadius: 3)
                var shackle = Path()
                shackle.move(to: CGPoint(x: 83, y: 134))
                shackle.addLine(to: CGPoint(x: 83, y: 128))
                shackle.addArc(center: CGPoint(x: 90, y: 128), radius: 7,
                               startAngle: .degrees(180), endAngle: .degrees(0), clockwise: false)
                shackle.addLine(to: CGPoint(x: 97, y: 134))
                layer.stroke(shackle, with: white, lineWidth: 2.2)
                layer.fill(body, with: .color(.black))
                layer.stroke(body, with: white, lineWidth: 2.2)
                layer.fill(Path(ellipseIn: CGRect(x: 88, y: 141, width: 4, height: 4)), with: white)
            }
        }

        ctx.draw(Text("neXal").font(.system(size: 24, weight: .medium)).tracking(1).foregroundColor(.white),
                 at: CGPoint(x: 90, y: 222))
        ctx.draw(Text("systems").font(.system(size: 12)).tracking(5).foregroundColor(.white.opacity(0.7)),
                 at: CGPoint(x: 92, y: 245))
        let gray = Color(white: 0.55)
        ctx.draw(Text("quantum-safe").font(.system(size: 11)).foregroundColor(gray), at: CGPoint(x: 90, y: 270))
        ctx.draw(Text("NIST level 5 encryption").font(.system(size: 11)).foregroundColor(gray),
                 at: CGPoint(x: 90, y: 285))

        var track = Path()
        track.move(to: CGPoint(x: 40, y: 312))
        track.addLine(to: CGPoint(x: 140, y: 312))
        ctx.stroke(track, with: .color(Color(white: 0.13)), lineWidth: 2)
        ctx.stroke(track.trimmedPath(from: 0, to: progress(t, Beat.bar)), with: white, lineWidth: 2)
    }

    /// One continuous outline so it can be drawn on with a trim.
    private static let cloudPath: Path = {
        var p = Path()
        p.move(to: CGPoint(x: 68, y: 110))
        p.addCurve(to: CGPoint(x: 70, y: 86), control1: CGPoint(x: 52, y: 110), control2: CGPoint(x: 54, y: 86))
        p.addCurve(to: CGPoint(x: 100, y: 82), control1: CGPoint(x: 74, y: 70), control2: CGPoint(x: 96, y: 68))
        p.addCurve(to: CGPoint(x: 112, y: 110), control1: CGPoint(x: 116, y: 82), control2: CGPoint(x: 124, y: 108))
        p.closeSubpath()
        return p
    }()
}
