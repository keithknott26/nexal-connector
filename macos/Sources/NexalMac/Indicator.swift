import Foundation

/// Shared vocabulary for the panel's always-visible indicators (HARDENING-PLAN
/// §26.3). Presentation only: nothing here reads the process, the network or the
/// configuration, so every state is testable without a Mac.
///
/// Colour is never the only signal. Every state below also names a distinct SF
/// Symbol and carries a text label plus a reason, so the row reads correctly in
/// grey, in a screenshot, and under VoiceOver.
enum IndicatorTone {
    /// Full colour: the affirmative state of this indicator.
    case colour
    /// Grey. For the RDMA indicator this means *slower, not unavailable* (§26.6)
    /// and must never be drawn as an error, a warning or a loss of function.
    /// Grey is the normal state on Thunderbolt 4 hardware.
    case grey
}

/// One indicator row. `reason` is always shown next to the label, because §26.3
/// forbids a grey state with no explanation. `detail` carries the longer honest
/// explanation and is shown in a disclosure — never dropped to save space.
/// `detailLines` carries an itemised breakdown for indicators whose underlying
/// facts are per-subsystem rather than one flag (§29.8); it appears in the same
/// disclosure, so a correct model needs no extra top-level indicator.
struct IndicatorState: Equatable {
    let heading: String
    let label: String
    let systemImage: String
    let tone: IndicatorTone
    let reason: String
    let detail: String?
    let detailLines: [String]

    init(heading: String, label: String, systemImage: String,
         tone: IndicatorTone, reason: String, detail: String? = nil,
         detailLines: [String] = []) {
        self.heading = heading
        self.label = label
        self.systemImage = systemImage
        self.tone = tone
        self.reason = reason
        self.detail = detail
        self.detailLines = detailLines
    }

    /// Spoken as one sentence so the symbol, the label and the reason arrive
    /// together rather than as three unrelated fragments.
    var accessibilityDescription: String { "\(heading): \(label). \(reason)" }
}
