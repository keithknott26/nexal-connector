import AppKit
import SwiftUI

/// What `nexal inference review-latest --json` prints.
struct ReviewLatest: Decodable, Equatable {
    let modelId: String
    let hfRepo: String
    let revision: String
    let license: String
    let gated: Bool
    let commitUrl: String
    let reviewedRevision: String
    let upToDate: Bool

    /// Only a huggingface.co page is ever opened from here.
    var commitPage: URL? {
        guard let u = URL(string: commitUrl), u.scheme == "https", u.host == "huggingface.co" else { return nil }
        return u
    }
}

/// One line of `nexal inference review --json`.
struct ReviewEvent: Decodable, Equatable {
    let event: String
    let file: String?
    let bytes: Int64?
    let total: Int64?
    let code: String?
    let message: String?
    let revision: String?
    let files: Int?
    let detail: String?
    let key: String?
}

/// Maintainer tool: look at the newest commit of a model, read it on Hugging Face, and with one click hash
/// that exact commit into the reviewed-pins.json that ships with the next build.
@MainActor
final class ModelReviewer: ObservableObject {
    struct Row: Equatable {
        var latest: ReviewLatest?
        var checking = false
        var hashing = false
        var fraction: Double?
        var file = ""
        var done: String?
        var problem: String?
    }

    @Published private(set) var rows: [String: Row] = [:]
    @Published private(set) var reviewed: [String: String] = [:]
    private var token: ProcessCancelToken?

    /// The reviewed-pins.json of a source checkout, when this Mac has one (so owners never see the tool).
    static var pinsFile: String? {
        let home = NSHomeDirectory()
        let candidates = [UserDefaults.standard.string(forKey: "maintainerReviewedPinsPath"),
                          home + "/Developer/nexal-connector/connector/internal/inference/reviewed-pins.json",
                          home + "/nexal-connector/connector/internal/inference/reviewed-pins.json"].compactMap { $0 }
        return candidates.first { $0.hasSuffix("/reviewed-pins.json") && FileManager.default.fileExists(atPath: $0) }
    }

    var isBusy: Bool { rows.values.contains { $0.hashing || $0.checking } }

    func row(_ id: String) -> Row { rows[id] ?? Row() }

    func loadReviewed(_ app: AppModel) async { reviewed = await app.reviewedRevisions() ?? reviewed }

    func check(_ id: String, app: AppModel) {
        guard !row(id).checking else { return }
        rows[id] = Row(checking: true)
        Task {
            var r = Row()
            do {
                r.latest = try JSONDecoder().decode(ReviewLatest.self, from: try await app.reviewLatest(modelId: id))
            } catch { r.problem = "Could not read the latest commit: \(error.localizedDescription)" }
            rows[id] = r
        }
    }

    func approve(_ id: String, app: AppModel) {
        guard let latest = row(id).latest, let out = Self.pinsFile, !isBusy else { return }
        var r = Row(latest: latest)
        r.hashing = true
        rows[id] = r
        Task {
            defer { token = nil }
            do {
                let command = try app.reviewCommand(modelId: id, revision: latest.revision, out: out)
                let token = ProcessCancelToken()
                self.token = token
                var continuation: AsyncStream<Data>.Continuation!
                let lines = AsyncStream<Data> { continuation = $0 }
                let sink = continuation!
                let run = Task<ConnectorStreamResult, Error> {
                    defer { sink.finish() }
                    return try await app.runInferenceCommand(command, token: token) { _ = sink.yield($0) }
                }
                for await line in lines { apply(try? JSONDecoder().decode(ReviewEvent.self, from: line), to: id) }
                let result = try await run.value
                var end = row(id)
                end.hashing = false
                if result.status != 0, end.problem == nil { end.problem = result.reason ?? "The review stopped without saying why." }
                rows[id] = end
                await loadReviewed(app)
            } catch {
                var end = row(id)
                end.hashing = false
                end.problem = error.localizedDescription
                rows[id] = end
            }
        }
    }

    func cancel() { token?.cancel() }

    private func apply(_ e: ReviewEvent?, to id: String) {
        guard let e else { return }
        var r = row(id)
        switch e.event {
        case "progress":
            r.file = e.file ?? r.file
            if let b = e.bytes, let t = e.total, t > 0 { r.fraction = min(max(Double(b) / Double(t), 0), 1) }
        case "blocker":
            r.problem = "Refused: \(e.file ?? "a file") has \(e.key ?? "metadata") the runtime does not allow. \(e.detail ?? "")"
        case "error":
            if r.problem == nil { r.problem = e.message ?? "The review failed." }
        case "done":
            r.done = "Added \(String((e.revision ?? "").prefix(7))) (\(e.files ?? 0) files) to reviewed-pins.json. Commit it to ship it."
            r.hashing = false
        default: break
        }
        rows[id] = r
    }
}

struct ModelReviewView: View {
    @EnvironmentObject private var app: AppModel
    @StateObject private var reviewer = ModelReviewer()

    var body: some View {
        if let path = ModelReviewer.pinsFile {
            VStack(alignment: .leading, spacing: 8) {
                Divider()
                Text("REVIEW MODELS FOR THE NEXT RELEASE").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
                Text("Check a model\u{2019}s newest commit, read it on Hugging Face, then approve it. The exact commit and every file\u{2019}s hash are added to reviewed-pins.json, so people get the model with one click. Commit that file to ship it.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                ForEach(InferenceQuickStart.models) { item in modelRow(item) }
                Button("Show reviewed-pins.json") { NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)]) }
                    .buttonStyle(.link).font(.caption2)
            }
            .task { await reviewer.loadReviewed(app) }
        }
    }

    @ViewBuilder private func modelRow(_ item: QuickStartModel) -> some View {
        let r = reviewer.row(item.id)
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                VStack(alignment: .leading, spacing: 1) {
                    Text(item.title).font(.callout)
                    if let rev = reviewer.reviewed[item.id] {
                        Text("Reviewed at \(rev.prefix(7))").font(.caption2).foregroundStyle(.green)
                    } else {
                        Text("Not reviewed yet").font(.caption2).foregroundStyle(.secondary)
                    }
                }
                Spacer()
                if r.checking { ProgressView().controlSize(.small) }
                Button("Check latest") { reviewer.check(item.id, app: app) }
                    .controlSize(.small).disabled(reviewer.isBusy)
            }
            if let l = r.latest {
                HStack(spacing: 6) {
                    Text("Latest \(l.revision.prefix(7)) · \(l.license.isEmpty ? "no license listed" : l.license)").font(.caption2)
                    if let page = l.commitPage { Link("Read the commit", destination: page).font(.caption2) }
                }
                if l.upToDate {
                    Label("Already reviewed at this commit", systemImage: "checkmark.circle.fill").font(.caption2).foregroundStyle(.green)
                } else if l.gated {
                    Label("This repository is gated; it cannot be reviewed.", systemImage: "lock.fill").font(.caption2).foregroundStyle(.orange)
                } else if r.hashing {
                    VStack(alignment: .leading, spacing: 2) {
                        if let f = r.fraction { ProgressView(value: f) } else { ProgressView().progressViewStyle(.linear) }
                        Text("Hashing \(r.file)…").font(.caption2).foregroundStyle(.secondary)
                        Button("Cancel") { reviewer.cancel() }.controlSize(.small)
                    }
                } else if r.done == nil {
                    Button("I read it \u{2013} approve this commit") { reviewer.approve(item.id, app: app) }
                        .buttonStyle(.borderedProminent).controlSize(.small).disabled(reviewer.isBusy)
                }
            }
            if let d = r.done { Label(d, systemImage: "checkmark.seal.fill").font(.caption2).foregroundStyle(.green).fixedSize(horizontal: false, vertical: true) }
            if let p = r.problem { Label(p, systemImage: "exclamationmark.triangle.fill").font(.caption2).foregroundStyle(.orange).textSelection(.enabled).fixedSize(horizontal: false, vertical: true) }
        }
    }
}
