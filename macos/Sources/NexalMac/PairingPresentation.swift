import Foundation

/// Presentation only (HARDENING-PLAN §26). Go remains authoritative for the
/// pairing itself: it mints it, it owns the expiry, it reports the status and it
/// is the only QR encoder in the system. Nothing here talks to a coordinator,
/// computes an expiry of its own, or decides that a pairing is dead — a countdown
/// reaching zero on screen is a hint to poll, never a claim about server state.
///
/// Everything in this file is testable without a Mac, without a network and
/// without the connector, which is the point: the pairing panel's behaviour is
/// exercised by unit tests even though the app itself cannot be compiled in the
/// environment this was written in.
struct PairingPresentation: Equatable {
    /// The four states apps/coordinator/src/home.ts can report, plus the local
    /// "nothing has been minted" state. An unrecognised string from a newer
    /// connector becomes `.unknown(raw)` rather than being silently mapped to a
    /// state the app understands; showing "waiting" for something the connector
    /// called something else would be a lie about server state.
    enum Status: Equatable {
        case waiting, scanned, cancelled, expired
        case unknown(String)

        init(raw: String) {
            switch raw {
            case "waiting": self = .waiting
            case "scanned": self = .scanned
            case "cancelled": self = .cancelled
            case "expired": self = .expired
            default: self = .unknown(raw)
            }
        }

        var raw: String {
            switch self {
            case .waiting: return "waiting"
            case .scanned: return "scanned"
            case .cancelled: return "cancelled"
            case .expired: return "expired"
            case let .unknown(value): return value
            }
        }

        /// Whether this pairing can still be scanned. The QR is dimmed rather than
        /// removed when it cannot: an empty box tells the owner nothing about why.
        var isLive: Bool { self == .waiting }

        var isTerminal: Bool { self != .waiting }

        var label: String {
            switch self {
            case .waiting: return "Waiting for your phone"
            case .scanned: return "Scanned by your phone"
            case .cancelled: return "Cancelled"
            case .expired: return "Expired"
            case .unknown: return "Reported an unfamiliar state"
            }
        }

        var explanation: String {
            switch self {
            case .waiting:
                return "The coordinator is holding this pairing open. Scan the code in nexal@home."
            case .scanned:
                return "The phone claimed this pairing. Claiming is not linking: nothing is shared until you approve it on the phone."
            case .cancelled:
                return "This pairing was cancelled and cannot be scanned. Create a new one if you still want to pair."
            case .expired:
                return "The pairing window closed before a phone scanned it. Create a new one; codes are short-lived on purpose."
            case let .unknown(value):
                return "The connector reported \"\(value)\", which this app does not recognise. Update the app and connector together."
            }
        }

        var systemImage: String {
            switch self {
            case .waiting: return "qrcode.viewfinder"
            case .scanned: return "checkmark.circle"
            case .cancelled: return "slash.circle"
            case .expired: return "clock.badge.exclamationmark"
            case .unknown: return "questionmark.circle"
            }
        }
    }

    let pairingId: String
    let role: PairingRole?
    /// The role exactly as the connector reported it, kept even when it is not one
    /// of the two known roles, so the panel can say what it actually got.
    let rawRole: String
    let coordinator: String
    let status: Status
    let expiresAt: Date?
    let symbol: PairingSymbol?

    /// Builds the presentation from the CLI's mint record. Returns nil when the
    /// record cannot be displayed honestly (an unusable matrix, an unparseable
    /// expiry), because drawing a placeholder code that no phone can read is worse
    /// than drawing nothing and saying why.
    init?(mint: PairingMint) {
        let pairing = mint.pairing
        guard let symbol = PairingSymbol(qr: pairing.qr) else { return nil }
        pairingId = pairing.pairingId
        rawRole = pairing.role
        role = PairingRole(rawValue: pairing.role)
        coordinator = pairing.coordinator
        status = Status(raw: pairing.status)
        expiresAt = PairingPresentation.date(from: pairing.expiresAt)
        self.symbol = symbol
    }

    /// Applies a later `pair --status` poll. The symbol and the role are carried
    /// forward: a status poll says nothing about them, and re-rendering a code
    /// from a status record would mean inventing one.
    func updated(with state: PairingStatusReport.State) -> PairingPresentation {
        PairingPresentation(
            pairingId: pairingId, role: role, rawRole: rawRole, coordinator: coordinator,
            status: state.pairingId == pairingId ? Status(raw: state.status) : status,
            expiresAt: PairingPresentation.date(from: state.expiresAt) ?? expiresAt,
            symbol: symbol)
    }

    init(pairingId: String, role: PairingRole?, rawRole: String, coordinator: String,
         status: Status, expiresAt: Date?, symbol: PairingSymbol?) {
        self.pairingId = pairingId
        self.role = role
        self.rawRole = rawRole
        self.coordinator = coordinator
        self.status = status
        self.expiresAt = expiresAt
        self.symbol = symbol
    }

    /// Whole seconds left, floored, or nil when the connector did not give a
    /// parseable expiry. Never negative: a negative countdown reads as a bug.
    func secondsRemaining(now: Date = Date()) -> Int? {
        guard let expiresAt else { return nil }
        return max(0, Int(expiresAt.timeIntervalSince(now).rounded(.down)))
    }

    /// The countdown line. It is phrased as an observation about the code, not as
    /// a claim about the coordinator, because only the coordinator knows whether a
    /// pairing is still live.
    func countdown(now: Date = Date()) -> String {
        guard status.isLive else { return status.label }
        guard let seconds = secondsRemaining(now: now) else {
            return "The connector did not report a readable expiry for this code."
        }
        if seconds == 0 { return "This code has reached its expiry. Refresh to confirm with the coordinator." }
        let minutes = seconds / 60
        let remainder = seconds % 60
        let time = minutes > 0 ? "\(minutes)m \(remainder)s" : "\(remainder)s"
        return "Expires in \(time)."
    }

    /// The role line, which must stay honest even for a role this build does not
    /// know: the owner needs to see what the connector actually said.
    var roleLine: String {
        guard let role else { return "Role reported as \"\(rawRole)\", which this app does not recognise." }
        return "\(role.label). \(role.explanation)"
    }

    var indicator: IndicatorState {
        IndicatorState(
            heading: "Phone pairing",
            label: status.label,
            systemImage: status.systemImage,
            tone: status == .waiting || status == .scanned ? .colour : .grey,
            reason: status.explanation,
            detail: "Pairing id \(pairingId). Minted by the Go connector against \(coordinator); this app never contacts a coordinator itself.",
            detailLines: [roleLine])
    }

    /// Why pairing cannot be started, in the order the owner has to fix them, or
    /// nil when it can. §26: a dead button with no stated cause reads as a broken
    /// app, so the panel shows this text and the button stays visible.
    ///
    /// The reasons that can only be discovered by asking the coordinator (the
    /// pairing feature being switched off, a revoked host credential) are NOT
    /// guessed here — they arrive as the connector's own error text.
    static func unavailableReason(hasExecutable: Bool, configurationExists: Bool,
                                  status: ConnectorStatus?) -> String? {
        if !hasExecutable { return "Choose the Go connector under Setup before pairing a phone." }
        if !configurationExists { return "Create and enroll a connector configuration before pairing a phone." }
        guard let status else {
            return "Start or connect the connector first; pairing needs its enrolled host credential."
        }
        if status.hostId?.isEmpty != false {
            return "Enroll this Mac before pairing a phone. Pairing is authenticated with the enrolled host credential."
        }
        return nil
    }

    /// The CLI reports RFC3339 UTC, with or without fractional seconds — the same
    /// two forms WireDate.parse accepts on the phone.
    static func date(from raw: String) -> Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: raw) { return date }
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: raw)
    }
}

/// The module matrix, validated and padded with its quiet zone.
///
/// A QR without a quiet zone is a QR that many scanners simply will not see, and
/// the border is the kind of detail that is invisible in a code review and fatal
/// in a camera viewfinder. The Go renderer pads its terminal output; this type
/// pads the on-screen one, from the same `quietZone` value the CLI reports.
struct PairingSymbol: Equatable {
    let version: Int
    let mask: Int
    /// Side length WITHOUT the quiet zone, as the encoder counts it.
    let size: Int
    let quietZone: Int
    let errorLevel: String
    /// Row-major, true for a dark module, quiet zone NOT included.
    let modules: [[Bool]]

    init?(qr: PairingMint.Pairing.Symbol) {
        // Every bound here is a property of the format rather than a guess:
        // versions run 1...40, a version-v symbol is 4v+17 modules on a side, and
        // the quiet zone is 4 in the specification. A record that violates any of
        // them is not a symbol this app can draw, so it draws nothing.
        guard qr.version >= 1, qr.version <= 40,
              qr.mask >= 0, qr.mask <= 7,
              qr.size == 4 * qr.version + 17,
              qr.quietZone >= 0, qr.quietZone <= 8,
              qr.moduleRows.count == qr.size else { return nil }
        var rows: [[Bool]] = []
        rows.reserveCapacity(qr.size)
        for row in qr.moduleRows {
            guard row.count == qr.size else { return nil }
            var cells: [Bool] = []
            cells.reserveCapacity(qr.size)
            for character in row {
                switch character {
                case "1": cells.append(true)
                case "0": cells.append(false)
                default: return nil
                }
            }
            rows.append(cells)
        }
        version = qr.version
        mask = qr.mask
        size = qr.size
        quietZone = qr.quietZone
        errorLevel = qr.errorLevel
        modules = rows
    }

    /// Side length including the quiet zone on both sides.
    var paddedSize: Int { size + 2 * quietZone }

    /// The matrix with its quiet zone, which is what should actually be drawn.
    var paddedModules: [[Bool]] {
        let blank = [Bool](repeating: false, count: paddedSize)
        var padded = [[Bool]](repeating: blank, count: quietZone)
        for row in modules {
            padded.append([Bool](repeating: false, count: quietZone) + row
                          + [Bool](repeating: false, count: quietZone))
        }
        padded.append(contentsOf: [[Bool]](repeating: blank, count: quietZone))
        return padded
    }

    /// Shown under the code so the owner can report what failed to scan. It is
    /// also the cheapest way to notice that the app and connector disagree.
    var caption: String {
        "QR version \(version), error correction \(errorLevel), mask \(mask), \(size)×\(size) modules."
    }
}
