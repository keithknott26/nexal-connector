import Foundation

/// The report `nexal inference preflight --json` prints (connector/internal/inference,
/// CLI-CONTRACT.md "AI inference preflight"). This file holds the decoding and the
/// wording of the "AI Inference" section and nothing else: no view, no process, no
/// state, so every sentence the panel shows can be unit tested.
///
/// The check is READ-ONLY. It installs nothing. Everything it could not see is in
/// `gaps` and is shown, never filled in.
enum InferenceReportError: LocalizedError, Equatable {
    case unsupportedSchema(Int)
    case unreadable

    var errorDescription: String? {
        switch self {
        case let .unsupportedSchema(version):
            return "The check reported format \(version), which this version of the app does not understand. Update neXal."
        case .unreadable:
            return "The check returned something this app could not read. Update neXal@home and try again."
        }
    }
}

/// Per-model result. An unknown value decodes to `.other` so an additive change in
/// the connector cannot make the whole report unreadable.
enum InferenceVerdict: Equatable, Decodable {
    case runsNow, fitsSingleHostBlocked, fitsShardedOnly, doesNotFit
    case other(String)

    init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        switch raw {
        case "runs_now": self = .runsNow
        case "fits_single_host_blocked": self = .fitsSingleHostBlocked
        case "fits_sharded_only": self = .fitsShardedOnly
        case "does_not_fit": self = .doesNotFit
        default: self = .other(raw)
        }
    }
}

enum InferenceSeverity: Equatable, Decodable {
    case blocker, warning, info
    case other(String)

    init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        switch raw {
        case "blocker": self = .blocker
        case "warning": self = .warning
        case "info": self = .info
        default: self = .other(raw)
        }
    }

    /// Blockers first, then warnings, then facts.
    var order: Int {
        switch self {
        case .blocker: return 0
        case .warning: return 1
        case .info: return 2
        case .other: return 3
        }
    }
}

struct InferenceReport: Decodable, Equatable {
    /// The only report format this build understands.
    static let supportedSchemaVersion = 1

    let schemaVersion: Int
    let generatedAt: String
    let headline: String
    let machines: [Machine]
    let links: [Link]
    let models: [Model]
    let recommendation: Recommendation
    let sharing: Sharing
    let blockers: [Blocker]
    let gaps: [String]
    let assumptions: [String]
    let catalogNote: String
    let surface: String

    struct Runtime: Decodable, Equatable {
        let state: String
        let detail: String
        let version: String?
        let mlxVersion: String?
        let mlxLmVersion: String?
        let distributedExecution: String?
        /// True only when the connector really probed the runtime on that Mac.
        let verified: Bool
    }

    struct Machine: Decodable, Equatable, Identifiable {
        let id: String
        let name: String
        let isSelf: Bool
        let online: Bool
        let chip: String?
        let os: String?
        let totalMemoryBytes: UInt64
        let availableMemoryBytes: UInt64
        let approvedMemoryBytes: UInt64
        let ownerReserveBytes: UInt64
        let headroomBytes: UInt64
        let usableBytes: UInt64
        /// local-policy | assumed-peer-defaults | unavailable
        let admissionSource: String
        let diskFreeBytes: UInt64
        let diskKnown: Bool
        let runtime: Runtime
        let eligible: Bool
        let reasons: [String]
    }

    struct Link: Decodable, Equatable, Identifiable {
        let peerId: String
        let peerName: String
        let online: Bool
        let path: String
        let pathLabel: String?
        let directVia: String?
        /// Measured values are absent when nothing measured them; never zero.
        let latencyMs: Double?
        let bandwidthMbps: Double?
        let packetLossPercent: Double?
        let pq: String
        let pqReason: String?
        let pathFlapsLastHour: Int
        let unstable: Bool
        let transport: String
        let rdma: String
        /// good | fair | poor | unusable
        let quality: String
        let notes: [String]

        var id: String { peerId }
    }

    struct RankLayout: Decodable, Equatable {
        let rank: Int
        let machineId: String
        let machineName: String
        let requiredBytes: Int64
        let usableBytes: Int64
    }

    struct Model: Decodable, Equatable, Identifiable {
        let id: String
        let displayName: String
        let paramsBillions: Double
        let quantization: String
        let verdict: InferenceVerdict
        let summary: String
        let requiredBytesSingleHost: Int64
        let hostId: String?
        let hostName: String?
        let layout: [RankLayout]?
        let layoutLinksOk: Bool?
        let fitsIfCapRaised: Bool?
        let reasons: [String]
        let installable: Bool
        let installBlockedReason: String?
        let modelProvisioning: String
        /// Download (and on-disk) size in bytes when the connector reports it; absent
        /// means unknown, never zero.
        let downloadBytes: Int64?
    }

    struct Recommendation: Decodable, Equatable {
        let modelId: String?
        let displayName: String?
        let runnableNow: Bool
        let hostName: String?
        let reason: String
        let howToUse: [String]
    }

    struct Sharing: Decodable, Equatable {
        let singleHostExecution: String
        let multiHostExecution: String
        let statement: String
        let evidence: [String]
    }

    struct Blocker: Decodable, Equatable, Identifiable {
        let code: String
        let severity: InferenceSeverity
        let scope: String
        let title: String
        let detail: String
        let fix: String
        let fixCommand: String?

        var id: String { code + "|" + scope }
    }

    /// Decodes the CLI's stdout. Refuses a format it does not know rather than
    /// guessing at renamed fields.
    static func decode(_ data: Data) throws -> InferenceReport {
        struct Header: Decodable { let schemaVersion: Int }
        guard let header = try? JSONDecoder().decode(Header.self, from: data) else {
            throw InferenceReportError.unreadable
        }
        guard header.schemaVersion == supportedSchemaVersion else {
            throw InferenceReportError.unsupportedSchema(header.schemaVersion)
        }
        do { return try JSONDecoder().decode(InferenceReport.self, from: data) }
        catch { throw InferenceReportError.unreadable }
    }
}

/// How a row should be coloured. The view maps this to colours; nothing here
/// depends on SwiftUI.
enum InferenceTone: Equatable {
    case good, caution, bad, neutral
}

/// Everything the "AI Inference" section says, derived from a decoded report.
struct InferencePresentation: Equatable {
    let report: InferenceReport

    init(_ report: InferenceReport) { self.report = report }

    // MARK: Fixed wording

    static let buttonTitle = "Add AI Inference Model"
    static let sectionTitle = "AI Inference"
    static let intro = "Checks what this Mac and your other Macs could run. The check only looks: nothing is downloaded until you confirm an install."
    /// What a person sees while the check runs.
    static let progressSteps = [
        "Reading this Mac and your other Macs",
        "Checking the links between them",
        "Probing the MLX runtime",
        "Working out which models fit",
    ]

    // MARK: Formatting

    /// Memory in GB (1 GB = 2^30 bytes, the way Activity Monitor counts memory).
    /// Locale-independent so tests are stable.
    static func memory(_ bytes: UInt64) -> String {
        String(format: "%.1f GB", Double(bytes) / 1_073_741_824)
    }

    static func memory(_ bytes: Int64) -> String { memory(UInt64(max(0, bytes))) }

    // MARK: Machines

    struct MachineRow: Equatable, Identifiable {
        let id: String
        let title: String
        let subtitle: String
        let memoryLine: String
        let runtimeLine: String
        let diskLine: String?
        let usable: Bool
        let tone: InferenceTone
        let reasons: [String]
    }

    var machineRows: [MachineRow] {
        report.machines.map { m in
            var memory = "\(Self.memory(m.totalMemoryBytes)) memory · \(Self.memory(m.usableBytes)) usable for a model"
            if m.admissionSource == "assumed-peer-defaults" {
                memory += " (estimated: another Mac's owner limits are not visible from here)"
            } else if m.admissionSource == "unavailable" {
                memory = "\(Self.memory(m.totalMemoryBytes)) memory · owner limits unavailable"
            }
            let subtitle = [m.chip, m.os].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
            return MachineRow(
                id: m.id,
                title: m.isSelf ? "\(m.name) (this Mac)" : m.name,
                subtitle: subtitle.isEmpty ? "Details not reported" : subtitle,
                memoryLine: memory,
                runtimeLine: Self.runtimeLine(m.runtime),
                diskLine: m.diskKnown ? "\(Self.memory(m.diskFreeBytes)) free disk" : nil,
                usable: m.eligible,
                tone: m.eligible ? (m.runtime.state == "healthy" || !m.isSelf ? .good : .caution) : .bad,
                reasons: m.reasons)
        }
    }

    static func runtimeLine(_ runtime: InferenceReport.Runtime) -> String {
        switch runtime.state {
        case "healthy": return "MLX runtime ready"
        case "not_configured": return "MLX runtime not set up"
        case "unverified": return "MLX runtime cannot be checked from here"
        case "integrity_mismatch": return "MLX runtime files do not match the owner's pins"
        case "unsupported_platform": return "MLX does not run on this Mac"
        case "dependencies_missing": return "MLX runtime is missing packages"
        case "version_mismatch": return "MLX packages are the wrong versions"
        default: return "MLX runtime: \(runtime.state.replacingOccurrences(of: "_", with: " "))"
        }
    }

    // MARK: Links

    struct LinkRow: Equatable, Identifiable {
        let id: String
        let title: String
        let qualityLabel: String
        let tone: InferenceTone
        let detail: String
        let transport: String
        let notes: [String]
    }

    var linkRows: [LinkRow] {
        // The existing RDMA wording is reused so this section never says anything
        // about RDMA that the transport panel would not.
        let rdma = RDMAPresentation(capability: .notReported(sourceName: "this check")).label
        return report.links.map { l in
            let path: String
            switch l.path {
            case "direct": path = l.directVia == "lan" ? "Direct, same network" : "Direct"
            case "relay": path = "Through a relay"
            case "cloud": path = "Through the cloud"
            default: path = "Path unknown"
            }
            var parts = [path]
            parts.append(l.latencyMs.map { String(format: "%.0f ms", $0) } ?? "latency not measured")
            parts.append(l.bandwidthMbps.map { String(format: "%.0f Mbps", $0) } ?? "bandwidth not measured")
            parts.append(l.pq == "protected" ? "post-quantum protected" : "post-quantum \(l.pq)")
            if l.pathFlapsLastHour > 0 {
                parts.append("switched path \(l.pathFlapsLastHour) \(l.pathFlapsLastHour == 1 ? "time" : "times") in the last hour")
            }
            let label: String
            let tone: InferenceTone
            switch l.quality {
            case "good": label = "Good link"; tone = .good
            case "fair": label = "Fair link"; tone = .caution
            case "poor": label = l.unstable ? "Unstable link" : "Poor link"; tone = .bad
            default: label = "Link down"; tone = .bad
            }
            return LinkRow(id: l.peerId, title: l.peerName, qualityLabel: label, tone: tone,
                           detail: parts.joined(separator: " · "),
                           transport: l.rdma == "unknown" ? rdma : "RDMA: \(l.rdma)",
                           notes: l.notes)
        }
    }

    // MARK: Models

    struct ModelRow: Equatable, Identifiable {
        let id: String
        let title: String
        let verdictLabel: String
        let summary: String
        let tone: InferenceTone
        let isRecommended: Bool
        let runnableNow: Bool
        let detail: [String]
    }

    var modelRows: [ModelRow] {
        report.models.map { m in
            let label: String
            let tone: InferenceTone
            var detail = m.reasons
            switch m.verdict {
            case .runsNow:
                let host = m.hostName.map { " on \($0)" } ?? ""
                label = "Runs now" + host; tone = .good
            case .fitsSingleHostBlocked:
                label = "Fits, but blocked"; tone = .caution
            case .fitsShardedOnly:
                label = "Only when split — not runnable yet"; tone = .caution
                for r in m.layout ?? [] {
                    detail.append("Part \(r.rank + 1) on \(r.machineName): about \(Self.memory(r.requiredBytes))")
                }
                if m.layoutLinksOk == false {
                    detail.append("A link in this layout is relayed, unstable or down.")
                }
            case .doesNotFit:
                label = "Does not fit"; tone = .bad
            case .other:
                label = "Unknown result"; tone = .neutral
            }
            return ModelRow(id: m.id, title: m.displayName, verdictLabel: label, summary: m.summary, tone: tone,
                            isRecommended: m.id == report.recommendation.modelId && m.id != "",
                            runnableNow: m.verdict == .runsNow, detail: detail)
        }
    }

    // MARK: Recommendation

    struct RecommendationCard: Equatable {
        let title: String
        let subtitle: String
        let runnableNow: Bool
        let howToUse: [String]
    }

    /// nil when no catalog model fits at all.
    var recommendationCard: RecommendationCard? {
        let r = report.recommendation
        guard let name = r.displayName, let id = r.modelId, !id.isEmpty else { return nil }
        let where_ = r.hostName.map { " on \($0)" } ?? ""
        return RecommendationCard(
            title: r.runnableNow ? "Most advanced model you can run: \(name)\(where_)"
                                 : "Largest model your hardware can hold: \(name)\(where_)",
            subtitle: r.reason, runnableNow: r.runnableNow, howToUse: r.howToUse)
    }

    // MARK: Blockers

    struct BlockerRow: Equatable, Identifiable {
        let id: String
        let severityLabel: String
        let tone: InferenceTone
        let title: String
        let detail: String
        let fix: String
        let command: String?
    }

    /// Blockers, then warnings, then facts; report order is kept inside each group.
    var blockerRows: [BlockerRow] {
        let indexed = report.blockers.enumerated().sorted {
            ($0.element.severity.order, $0.offset) < ($1.element.severity.order, $1.offset)
        }
        return indexed.map { pair in
            let b = pair.element
            let label: String
            let tone: InferenceTone
            switch b.severity {
            case .blocker: label = "Blocker"; tone = .bad
            case .warning: label = "Warning"; tone = .caution
            case .info, .other: label = "Note"; tone = .neutral
            }
            return BlockerRow(id: b.id, severityLabel: label, tone: tone, title: b.title, detail: b.detail,
                              fix: b.fix, command: b.fixCommand)
        }
    }

    // MARK: Sharing and gaps

    /// The connector's own sentence, verbatim. The app never rewords what is
    /// available: multi-host execution is not built.
    var sharingStatement: String { report.sharing.statement }

    var gapLines: [String] { report.gaps }
}
