import Foundation

/// Decoding and wording for the model install flow: `nexal inference install`
/// (NDJSON events), `status`, `verify` and `remove`. Like InferenceReport.swift this
/// file holds no view, process or state, so every sentence can be unit tested.
///
/// Honesty rule: a model that was downloaded and hash-verified is NOT thereby
/// runnable. Only the connector's own `state` decides what is claimed.
enum InferenceInstallError: LocalizedError, Equatable {
    case unsupportedSchema(Int)
    case unreadable
    case invalidModelID

    var errorDescription: String? {
        switch self {
        case let .unsupportedSchema(version):
            return "The installed-models list uses format \(version), which this version of the app does not understand. Update neXal."
        case .unreadable:
            return "The connector returned something this app could not read. Update neXal@home and try again."
        case .invalidModelID:
            return "That model name is not valid."
        }
    }
}

/// A catalog model id such as `qwen3-14b-q4`. Checked before it reaches argv so a
/// value can never be read as a flag.
enum InferenceModelID {
    static func isValid(_ id: String) -> Bool {
        guard (1...128).contains(id.utf8.count), let first = id.unicodeScalars.first,
              CharacterSet.alphanumerics.contains(first), first.isASCII else { return false }
        return id.unicodeScalars.allSatisfy {
            $0.isASCII && (CharacterSet.alphanumerics.contains($0) || $0 == "-" || $0 == "." || $0 == "_")
        }
    }
}

/// Installed-model state as the connector reports it. The connector emits only
/// `installed_unmeasured` and `damaged`; anything else is shown verbatim and never as good.
enum InferenceModelState: Equatable, Decodable {
    case installedUnmeasured, damaged
    case other(String)

    init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        switch raw {
        case "installed_unmeasured": self = .installedUnmeasured
        case "damaged": self = .damaged
        default: self = .other(raw)
        }
    }

    /// What the app says. `installed_unmeasured` must never read as runnable.
    var label: String {
        switch self {
        case .installedUnmeasured:
            return "Installed and verified; not runnable until runtime release gates are completed"
        case .damaged: return "Damaged: files are missing or changed since install. Run Verify for details"
        case let .other(raw): return "State reported by the connector: \(raw)"
        }
    }

    var tone: InferenceTone {
        switch self {
        case .installedUnmeasured: return .caution
        case .damaged: return .bad
        case .other: return .neutral
        }
    }
}

// MARK: Install events (NDJSON)

struct InferenceInstallSummary: Equatable {
    let modelId: String
    let dir: String
    let manifestSha256: String
    let state: InferenceModelState
    let howToUse: [String]
}

enum InferenceInstallEvent: Equatable {
    case start(modelId: String, totalBytes: Int64)
    case progress(file: String, bytes: Int64, total: Int64, overallBytes: Int64, overallTotal: Int64)
    case verified(file: String)
    case done(InferenceInstallSummary)
    case error(code: String, message: String, fix: String?)
    /// An event this build does not know. Ignored, never fatal.
    case other(String)

    private struct Raw: Decodable {
        let event: String
        let modelId: String?
        let totalBytes: Int64?
        let file: String?
        let bytes: Int64?
        let total: Int64?
        let overallBytes: Int64?
        let overallTotal: Int64?
        let dir: String?
        let manifestSha256: String?
        let state: InferenceModelState?
        let howToUse: [String]?
        let code: String?
        let message: String?
        let fix: String?
    }

    /// Decodes one NDJSON line.
    static func decode(line: Data) throws -> InferenceInstallEvent {
        guard let raw = try? JSONDecoder().decode(Raw.self, from: line) else {
            throw InferenceInstallError.unreadable
        }
        switch raw.event {
        case "start":
            guard let id = raw.modelId, let total = raw.totalBytes else { throw InferenceInstallError.unreadable }
            return .start(modelId: id, totalBytes: total)
        case "progress":
            guard let file = raw.file, let bytes = raw.bytes, let total = raw.total,
                  let overall = raw.overallBytes, let overallTotal = raw.overallTotal else {
                throw InferenceInstallError.unreadable
            }
            return .progress(file: file, bytes: bytes, total: total, overallBytes: overall, overallTotal: overallTotal)
        case "verified":
            guard let file = raw.file else { throw InferenceInstallError.unreadable }
            return .verified(file: file)
        case "done":
            guard let id = raw.modelId, let state = raw.state else { throw InferenceInstallError.unreadable }
            return .done(InferenceInstallSummary(
                modelId: id, dir: raw.dir ?? "", manifestSha256: raw.manifestSha256 ?? "",
                state: state, howToUse: raw.howToUse ?? []))
        case "error":
            return .error(code: raw.code ?? "unknown",
                          message: raw.message ?? "The install failed and the connector gave no reason.",
                          fix: raw.fix)
        default:
            return .other(raw.event)
        }
    }
}

/// Live download progress, built by applying events in order.
struct InstallProgress: Equatable {
    var modelId: String
    var currentFile: String?
    var fileBytes: Int64 = 0
    var fileTotal: Int64 = 0
    var overallBytes: Int64 = 0
    var overallTotal: Int64 = 0
    var verifiedFiles: [String] = []
    /// Smoothed overall throughput; nil until two samples are far enough apart.
    var bytesPerSecond: Double?
    var lastSampleBytes: Int64?
    var lastSampleAt: Date?

    init(modelId: String) { self.modelId = modelId }

    /// Minimum spacing between throughput samples, so bursts of events do not
    /// produce a jumping number.
    static let sampleInterval: TimeInterval = 0.5

    mutating func apply(_ event: InferenceInstallEvent, at now: Date = Date()) {
        switch event {
        case let .start(id, total):
            modelId = id
            overallTotal = max(total, 0)
        case let .progress(file, bytes, total, overall, overallTotalBytes):
            currentFile = file
            fileBytes = max(bytes, 0)
            fileTotal = max(total, 0)
            // Overall progress never moves backwards, even if a file restarts.
            overallBytes = max(overallBytes, max(overall, 0))
            if overallTotalBytes > 0 { overallTotal = overallTotalBytes }
            sample(now)
        case let .verified(file):
            if !verifiedFiles.contains(file) { verifiedFiles.append(file) }
        case .done, .error, .other:
            break
        }
    }

    private mutating func sample(_ now: Date) {
        guard let at = lastSampleAt, let last = lastSampleBytes else {
            lastSampleAt = now; lastSampleBytes = overallBytes
            return
        }
        let dt = now.timeIntervalSince(at)
        guard dt >= Self.sampleInterval else { return }
        let instant = Double(max(overallBytes - last, 0)) / dt
        bytesPerSecond = bytesPerSecond.map { 0.7 * $0 + 0.3 * instant } ?? instant
        lastSampleAt = now; lastSampleBytes = overallBytes
    }

    /// 0...1.
    var fraction: Double {
        guard overallTotal > 0 else { return 0 }
        return min(max(Double(overallBytes) / Double(overallTotal), 0), 1)
    }

    var percentLabel: String { "\(Int((fraction * 100).rounded(.down)))%" }

    var sizeLabel: String {
        overallTotal > 0
            ? "\(InferencePresentation.memory(overallBytes)) of \(InferencePresentation.memory(overallTotal))"
            : InferencePresentation.memory(overallBytes)
    }

    /// "12.3 MB/s", or nil while it is still being measured.
    var speedLabel: String? {
        bytesPerSecond.map { String(format: "%.1f MB/s", $0 / 1_048_576) }
    }
}

// MARK: Installed models (`inference status --json`)

struct InferenceInstalledModel: Decodable, Equatable, Identifiable {
    let modelId: String
    let displayName: String?
    let state: InferenceModelState
    let revision: String
    let bytesOnDisk: Int64
    let installedAt: String?
    /// True when memory use is an estimate, not something measured on this Mac.
    let estimatedMemory: Bool
    let problems: [String]

    var id: String { modelId }
    var title: String { (displayName?.isEmpty == false ? displayName : nil) ?? modelId }
}

/// A leftover partial download (`models/.<id>.partial`). It is not an installed model.
struct InferenceIncompleteModel: Decodable, Equatable, Identifiable {
    let modelId: String
    let dir: String
    let bytesOnDisk: Int64

    var id: String { modelId }
}

struct InferenceInstalledReport: Decodable, Equatable {
    static let supportedSchemaVersion = 1

    let schemaVersion: Int
    let models: [InferenceInstalledModel]
    let incomplete: [InferenceIncompleteModel]

    private enum CodingKeys: String, CodingKey { case schemaVersion, models, incomplete }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try c.decode(Int.self, forKey: .schemaVersion)
        models = try c.decode([InferenceInstalledModel].self, forKey: .models)
        incomplete = try c.decodeIfPresent([InferenceIncompleteModel].self, forKey: .incomplete) ?? []
    }

    static func decode(_ data: Data) throws -> InferenceInstalledReport {
        struct Header: Decodable { let schemaVersion: Int }
        guard let header = try? JSONDecoder().decode(Header.self, from: data) else {
            throw InferenceInstallError.unreadable
        }
        guard header.schemaVersion == supportedSchemaVersion else {
            throw InferenceInstallError.unsupportedSchema(header.schemaVersion)
        }
        do { return try JSONDecoder().decode(InferenceInstalledReport.self, from: data) }
        catch { throw InferenceInstallError.unreadable }
    }
}

/// `inference verify <id> --json`. The CLI exits 1 with `ok:false` but still prints this.
struct InferenceVerifyResult: Decodable, Equatable {
    static let supportedSchemaVersion = 1

    struct File: Decodable, Equatable {
        let name: String
        let status: String
    }

    let schemaVersion: Int
    let modelId: String
    let ok: Bool
    let state: InferenceModelState?
    let files: [File]
    let problems: [String]

    private enum CodingKeys: String, CodingKey { case schemaVersion, modelId, ok, state, files, problems }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        schemaVersion = try c.decode(Int.self, forKey: .schemaVersion)
        modelId = try c.decode(String.self, forKey: .modelId)
        ok = try c.decode(Bool.self, forKey: .ok)
        state = try c.decodeIfPresent(InferenceModelState.self, forKey: .state)
        files = try c.decodeIfPresent([File].self, forKey: .files) ?? []
        problems = try c.decodeIfPresent([String].self, forKey: .problems) ?? []
    }

    static func decode(_ data: Data) throws -> InferenceVerifyResult {
        struct Header: Decodable { let schemaVersion: Int }
        guard let header = try? JSONDecoder().decode(Header.self, from: data) else {
            throw InferenceInstallError.unreadable
        }
        guard header.schemaVersion == supportedSchemaVersion else {
            throw InferenceInstallError.unsupportedSchema(header.schemaVersion)
        }
        do { return try JSONDecoder().decode(InferenceVerifyResult.self, from: data) }
        catch { throw InferenceInstallError.unreadable }
    }

    var message: String {
        if ok { return "Verified: every file and the manifest match the install receipt." }
        let bad = files.filter { $0.status != "ok" }.map { "\($0.name): \($0.status.replacingOccurrences(of: "_", with: " "))" }
        let detail = problems + bad.filter { !problems.contains($0) }
        return "Verification found problems: " + (detail.isEmpty ? "no detail given." : detail.joined(separator: "; "))
    }
}

// MARK: Wording and the install offer

enum InferenceInstallText {
    static let hostLine = "Downloads come from huggingface.co at a pinned revision, and every file is checked against a pinned SHA-256."
    static let unavailableLine = "Sharing one model across several Macs and a web UI are not available yet."

    static func row(_ m: InferenceInstalledModel) -> [String] {
        var parts = [InferencePresentation.memory(m.bytesOnDisk) + " on disk",
                     "revision " + String(m.revision.prefix(12))]
        if let at = m.installedAt, !at.isEmpty { parts.append("installed " + at) }
        var lines = [parts.joined(separator: " · ")]
        if m.estimatedMemory {
            lines.append("Memory use is an estimate; it has not been measured on this Mac.")
        }
        lines.append(contentsOf: m.problems)
        return lines
    }

    static func partialLine(_ m: InferenceIncompleteModel) -> String {
        "Partial download · \(InferencePresentation.memory(m.bytesOnDisk)) on disk · not installed"
    }

    static func removeMessage(_ m: InferenceInstalledModel) -> String {
        "This deletes the downloaded files for \(m.title) (\(InferencePresentation.memory(m.bytesOnDisk))) from this Mac. You can install it again later."
    }

    static func removePartialMessage(_ m: InferenceIncompleteModel) -> String {
        "This deletes the partial download of \(m.modelId) (\(InferencePresentation.memory(m.bytesOnDisk))). Nothing installed is affected."
    }
}

/// What the install button offers for the preflight's recommended model.
struct InferenceInstallOffer: Equatable {
    enum Availability: Equatable {
        case installable
        /// The connector's exact reason, shown verbatim.
        case blocked(reason: String)
        case doesNotFitThisMac
        case notEnoughDisk
    }

    let modelId: String
    let displayName: String
    /// nil when the connector did not report a download size; never guessed.
    let downloadBytes: Int64?
    let availability: Availability

    var buttonTitle: String {
        if let bytes = downloadBytes { return "Install \(displayName) (\(InferencePresentation.memory(bytes)))" }
        return "Install \(displayName)"
    }

    var sizeSentence: String {
        if let bytes = downloadBytes {
            let size = InferencePresentation.memory(bytes)
            return "This downloads about \(size) and uses about \(size) of disk space on this Mac."
        }
        return "The download size was not reported. It is shown as soon as the download starts, and you can cancel then."
    }

    var sourceSentence: String { InferenceInstallText.hostLine }

    var unavailableSentence: String? {
        switch availability {
        case .installable: return nil
        case let .blocked(reason): return reason
        case .doesNotFitThisMac: return "\(displayName) does not fit on this Mac, so it is not offered."
        case .notEnoughDisk: return "This Mac does not have enough free disk space for \(displayName)."
        }
    }

    /// nil when the report recommends nothing.
    static func make(from report: InferenceReport) -> InferenceInstallOffer? {
        guard let id = report.recommendation.modelId, !id.isEmpty,
              let model = report.models.first(where: { $0.id == id }) else { return nil }
        let size = model.downloadBytes
        let me = report.machines.first(where: { $0.isSelf })
        let availability: Availability
        if !model.installable {
            availability = .blocked(reason: model.installBlockedReason
                ?? "This model cannot be installed yet. The connector gave no reason.")
        } else if !(model.verdict == .runsNow || model.verdict == .fitsSingleHostBlocked)
                    || (model.hostId != nil && model.hostId != me?.id) {
            availability = .doesNotFitThisMac
        } else if let size, let me, me.diskKnown, me.diskFreeBytes < UInt64(max(size, 0)) {
            availability = .notEnoughDisk
        } else {
            availability = .installable
        }
        return InferenceInstallOffer(modelId: id, displayName: model.displayName,
                                     downloadBytes: size, availability: availability)
    }
}
