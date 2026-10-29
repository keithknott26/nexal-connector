import SwiftUI

/// `nexal overflow status` as JSON. API keys are never part of it, only whether one is stored.
struct OverflowStatus: Decodable, Equatable {
    struct Tier: Decodable, Equatable {
        var name: String
        var baseUrl: String
        var model: String
    }
    struct Settings: Decodable, Equatable {
        var enabled: Bool
        var hardAlias: String?
        var maxWaitSeconds: Int?
        var maxPromptTokens: Int?
        var dailyLimit: Int?
        var gpu: Tier?
        var cloud: Tier?
        var gpuPeers: [String]?
        var cloudPeers: [String]?
    }
    var overflow: Settings
    var gpuKeyStored: Bool
    var cloudKeyStored: Bool
}

/// Builds the `nexal overflow set` arguments from what the owner typed. Pure, so it is unit-tested.
enum OverflowArguments {
    static func set(enabled: Bool, hardAlias: String, maxWait: String, maxPrompt: String, dailyLimit: String,
                    gpuURL: String, gpuModel: String, cloudURL: String, cloudModel: String,
                    gpuPeers: String, cloudPeers: String) -> [String] {
        func number(_ s: String) -> String { String(Int(s.trimmingCharacters(in: .whitespaces)) ?? 0) }
        var a = ["--enabled=\(enabled)", "--hard-alias", hardAlias.trimmingCharacters(in: .whitespaces),
                 "--max-wait-seconds", number(maxWait), "--max-prompt-tokens", number(maxPrompt), "--daily-limit", number(dailyLimit),
                 "--gpu-peers", gpuPeers.trimmingCharacters(in: .whitespaces), "--cloud-peers", cloudPeers.trimmingCharacters(in: .whitespaces)]
        let gu = gpuURL.trimmingCharacters(in: .whitespaces), gm = gpuModel.trimmingCharacters(in: .whitespaces)
        if gu.isEmpty && gm.isEmpty { a.append("--remove-gpu") } else { a += ["--gpu-name", "gpu", "--gpu-url", gu, "--gpu-model", gm] }
        let cu = cloudURL.trimmingCharacters(in: .whitespaces), cm = cloudModel.trimmingCharacters(in: .whitespaces)
        if cu.isEmpty && cm.isEmpty { a.append("--remove-cloud") } else { a += ["--cloud-name", "cloud", "--cloud-url", cu, "--cloud-model", cm] }
        return a
    }
}

@MainActor
final class OverflowModel: ObservableObject {
    @Published var enabled = false
    @Published var hardAlias = "qwen-large"
    @Published var maxWait = "120"
    @Published var maxPrompt = "6000"
    @Published var dailyLimit = "200"
    @Published var gpuURL = ""
    @Published var gpuModel = ""
    @Published var gpuKey = ""
    @Published var gpuKeyStored = false
    @Published var gpuPeers = ""
    @Published var cloudURL = ""
    @Published var cloudModel = ""
    @Published var cloudKey = ""
    @Published var cloudKeyStored = false
    @Published var cloudPeers = ""
    @Published var message: String?
    @Published var busy = false

    func apply(_ s: OverflowStatus) {
        enabled = s.overflow.enabled
        hardAlias = s.overflow.hardAlias ?? hardAlias
        maxWait = String(s.overflow.maxWaitSeconds ?? 0)
        maxPrompt = String(s.overflow.maxPromptTokens ?? 0)
        dailyLimit = String(s.overflow.dailyLimit ?? 0)
        gpuURL = s.overflow.gpu?.baseUrl ?? ""
        gpuModel = s.overflow.gpu?.model ?? ""
        cloudURL = s.overflow.cloud?.baseUrl ?? ""
        cloudModel = s.overflow.cloud?.model ?? ""
        gpuPeers = (s.overflow.gpuPeers ?? []).joined(separator: ", ")
        cloudPeers = (s.overflow.cloudPeers ?? []).joined(separator: ", ")
        gpuKeyStored = s.gpuKeyStored
        cloudKeyStored = s.cloudKeyStored
    }

    func refresh(_ app: AppModel) async {
        do {
            let data = try await app.overflow(action: "status")
            apply(try JSONDecoder().decode(OverflowStatus.self, from: data))
        } catch { message = "Could not read the overflow settings: \(error.localizedDescription)" }
    }

    func save(_ app: AppModel) async {
        busy = true
        defer { busy = false }
        message = nil
        do {
            // Keys first, on stdin only; they are never put on a command line or in the config file.
            for (tier, key) in [("gpu", gpuKey), ("cloud", cloudKey)] where !key.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                _ = try await app.overflow(action: "set-key", args: ["--tier", tier], input: Data(key.trimmingCharacters(in: .whitespacesAndNewlines).utf8))
            }
            gpuKey = ""
            cloudKey = ""
            let args = OverflowArguments.set(enabled: enabled, hardAlias: hardAlias, maxWait: maxWait, maxPrompt: maxPrompt,
                                             dailyLimit: dailyLimit, gpuURL: gpuURL, gpuModel: gpuModel, cloudURL: cloudURL,
                                             cloudModel: cloudModel, gpuPeers: gpuPeers, cloudPeers: cloudPeers)
            let data = try await app.overflow(action: "set", args: args)
            apply(try JSONDecoder().decode(OverflowStatus.self, from: data))
            message = "Saved. Applies the next time a model starts on this Mac."
        } catch { message = "Not saved: \(error.localizedDescription)" }
    }

    func removeKey(_ tier: String, _ app: AppModel) async {
        _ = try? await app.overflow(action: "clear-key", args: ["--tier", tier])
        await refresh(app)
    }
}

/// Settings for sending requests this Mac's model cannot handle to a serverless GPU (and, if allowed, a cloud API).
struct OverflowSettingsView: View {
    @EnvironmentObject private var app: AppModel
    @StateObject private var model = OverflowModel()

    var body: some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: 8) {
                Text("When this Mac's model is busy or a request is large, send it to a bigger model on a serverless GPU (for example RunPod). If the GPU is too slow, a cloud API can answer instead. Off by default.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                Label("Privacy: a listed peer's prompt leaves your network for the provider you set below. Callers see which backend answered.", systemImage: "lock.open")
                    .font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
                Toggle("Use GPU / cloud overflow", isOn: $model.enabled).accessibilityIdentifier("overflow-enabled")
                tierFields(title: "SERVERLESS GPU", url: $model.gpuURL, modelId: $model.gpuModel, key: $model.gpuKey,
                           stored: model.gpuKeyStored, peers: $model.gpuPeers, tier: "gpu",
                           urlHint: "https://api.runpod.ai/v2/<endpoint id>/openai/v1")
                tierFields(title: "CLOUD API (LAST RESORT)", url: $model.cloudURL, modelId: $model.cloudModel, key: $model.cloudKey,
                           stored: model.cloudKeyStored, peers: $model.cloudPeers, tier: "cloud",
                           urlHint: "https://api.openai.com/v1")
                HStack {
                    field("Model name that asks for the GPU", $model.hardAlias)
                    field("Queue wait (s)", $model.maxWait)
                }
                HStack {
                    field("Long prompt (tokens)", $model.maxPrompt)
                    field("Daily request cap", $model.dailyLimit)
                }
                HStack {
                    Button("Save") { Task { await model.save(app) } }.buttonStyle(.borderedProminent).controlSize(.small).disabled(model.busy)
                    if let m = model.message { Text(m).font(.caption).foregroundStyle(.secondary) }
                    Spacer()
                }
            }
            .padding(.top, 6)
            .task { await model.refresh(app) }
        } label: {
            Text("GPU and cloud overflow").font(.caption.weight(.semibold))
        }
    }

    private func field(_ title: String, _ text: Binding<String>) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title).font(.caption2).foregroundStyle(.secondary)
            TextField("", text: text).textFieldStyle(.roundedBorder).controlSize(.small)
        }
    }

    @ViewBuilder private func tierFields(title: String, url: Binding<String>, modelId: Binding<String>, key: Binding<String>,
                                         stored: Bool, peers: Binding<String>, tier: String, urlHint: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
            field("Endpoint (\(urlHint))", url)
            field("Model", modelId)
            HStack(alignment: .bottom) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(stored ? "API key (saved in the Keychain; type to replace)" : "API key").font(.caption2).foregroundStyle(.secondary)
                    SecureField("", text: key).textFieldStyle(.roundedBorder).controlSize(.small)
                }
                if stored {
                    Button("Remove key") { Task { await model.removeKey(tier, app) } }.controlSize(.small)
                }
            }
            field("Peers allowed (neXal IPs, comma-separated, or *)", peers)
        }
    }
}
