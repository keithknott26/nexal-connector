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
        var always: Bool?
        var hardAlias: String?
        var maxWaitSeconds: Int?
        var maxPromptTokens: Int?
        var dailyLimit: Int?
        var gpu: Tier?
        var cloud: Tier?
        var gpuPeers: [String]?
        var cloudPeers: [String]?
        var runpod: RunPod?
    }
    /// What one-click setup created in the owner's RunPod account (ids only; the key stays in the Keychain).
    struct RunPod: Decodable, Equatable {
        var endpointId: String
        var templateId: String?
        var preset: String?
        var model: String
        var maxWorkers: Int?
        var createdAt: String?
    }
    var overflow: Settings
    var gpuKeyStored: Bool
    var cloudKeyStored: Bool
}

/// How traffic is offloaded from the model running on this Mac.
enum OverflowMode: String, CaseIterable, Identifiable {
    case off, busy, large, busyOrLarge, always
    var id: String { rawValue }
    var title: String {
        switch self {
        case .off: return "Off: keep everything on this Mac"
        case .busy: return "When this Mac is busy"
        case .large: return "Only large requests"
        case .busyOrLarge: return "When busy, and large requests"
        case .always: return "Always use the GPU"
        }
    }
    var explanation: String {
        switch self {
        case .off: return "Nothing leaves this Mac."
        case .busy: return "Requests that would wait longer than the limit below go to the GPU; the rest stay here."
        case .large: return "Requests with a prompt bigger than the limit below go to the GPU."
        case .busyOrLarge: return "Busy or large requests go to the GPU."
        case .always: return "Every request goes to the GPU (for the peers you allow). Costs the most."
        }
    }
    var usesWait: Bool { self == .busy || self == .busyOrLarge }
    var usesPrompt: Bool { self == .large || self == .busyOrLarge }

    static func from(_ s: OverflowStatus.Settings) -> OverflowMode {
        if !s.enabled { return .off }
        if s.always == true { return .always }
        let wait = (s.maxWaitSeconds ?? 0) > 0, prompt = (s.maxPromptTokens ?? 0) > 0
        if wait && prompt { return .busyOrLarge }
        if prompt { return .large }
        return .busy
    }
}

/// The ready-made GPU choices for one-click setup (the connector holds the model and GPU lists).
enum RunPodPreset: String, CaseIterable, Identifiable {
    case standard, light
    var id: String { rawValue }
    var title: String {
        switch self {
        case .standard: return "Standard: Qwen3 32B on a 48 GB GPU"
        case .light: return "Light: Qwen3 14B on a 24 GB GPU (cheaper)"
        }
    }
}

/// Builds the `nexal overflow set` arguments from what the owner typed. Pure, so it is unit-tested.
enum OverflowArguments {
    private static func number(_ s: String) -> String { String(max(0, Int(s.trimmingCharacters(in: .whitespaces)) ?? 0)) }

    /// `nexal overflow provision` arguments. The worker cap is the cost limit, so it is kept between 1 and 10.
    static func provision(preset: RunPodPreset, maxWorkers: String) -> [String] {
        let workers = min(10, max(1, Int(maxWorkers.trimmingCharacters(in: .whitespaces)) ?? 1))
        return ["--preset", preset.rawValue, "--max-workers", String(workers)]
    }

    /// How traffic is offloaded.
    static func offload(mode: OverflowMode, maxWait: String, maxPrompt: String) -> [String] {
        ["--enabled=\(mode != .off)", "--always=\(mode == .always)",
         "--max-wait-seconds", mode.usesWait ? number(maxWait) : "0",
         "--max-prompt-tokens", mode.usesPrompt ? number(maxPrompt) : "0"]
    }

    /// Where traffic goes (no keys: those travel on stdin).
    static func providers(hardAlias: String, dailyLimit: String, gpuURL: String, gpuModel: String, cloudURL: String,
                          cloudModel: String, gpuPeers: String, cloudPeers: String) -> [String] {
        var a = ["--hard-alias", hardAlias.trimmingCharacters(in: .whitespaces), "--daily-limit", number(dailyLimit),
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
    static let shared = OverflowModel()

    @Published var mode = OverflowMode.off
    @Published var hardAlias = "qwen-large"
    @Published var maxWait = "120"
    @Published var maxPrompt = "6000"
    @Published var dailyLimit = "200"
    @Published var gpuURL = ""
    @Published var gpuModel = ""
    @Published var gpuKey = ""
    @Published var gpuKeyStored = false
    @Published var gpuPeers = "*"
    @Published var cloudURL = ""
    @Published var cloudModel = ""
    @Published var cloudKey = ""
    @Published var cloudKeyStored = false
    @Published var cloudPeers = ""
    @Published var message: String?
    @Published var busy = false
    @Published var preset = RunPodPreset.standard
    @Published var maxWorkers = "1"
    @Published var runpod: OverflowStatus.RunPod?

    var gpuConfigured: Bool { gpuKeyStored && !gpuURL.isEmpty && !gpuModel.isEmpty }

    func apply(_ s: OverflowStatus) {
        mode = OverflowMode.from(s.overflow)
        hardAlias = s.overflow.hardAlias ?? hardAlias
        if let w = s.overflow.maxWaitSeconds, w > 0 { maxWait = String(w) }
        if let p = s.overflow.maxPromptTokens, p > 0 { maxPrompt = String(p) }
        dailyLimit = String(s.overflow.dailyLimit ?? 0)
        gpuURL = s.overflow.gpu?.baseUrl ?? ""
        gpuModel = s.overflow.gpu?.model ?? ""
        cloudURL = s.overflow.cloud?.baseUrl ?? ""
        cloudModel = s.overflow.cloud?.model ?? ""
        gpuPeers = (s.overflow.gpuPeers ?? []).joined(separator: ", ")
        cloudPeers = (s.overflow.cloudPeers ?? []).joined(separator: ", ")
        gpuKeyStored = s.gpuKeyStored
        cloudKeyStored = s.cloudKeyStored
        runpod = s.overflow.runpod
    }

    func refresh(_ app: AppModel) async {
        do {
            let data = try await app.overflow(action: "status")
            apply(try JSONDecoder().decode(OverflowStatus.self, from: data))
        } catch { message = "Could not read the offload settings: \(error.localizedDescription)" }
    }

    private func run(_ app: AppModel, args: [String], success: String) async {
        busy = true
        defer { busy = false }
        message = nil
        do {
            let data = try await app.overflow(action: "set", args: args)
            apply(try JSONDecoder().decode(OverflowStatus.self, from: data))
            message = success
        } catch { message = "Not saved: \(error.localizedDescription)" }
    }

    func saveOffload(_ app: AppModel) async {
        await run(app, args: OverflowArguments.offload(mode: mode, maxWait: maxWait, maxPrompt: maxPrompt),
                  success: "Saved. Applies the next time a model starts on this Mac.")
    }

    func saveProviders(_ app: AppModel) async {
        busy = true
        defer { busy = false }
        message = nil
        do {
            // Keys go on stdin only; they are never put on a command line or in the config file.
            for (tier, key) in [("gpu", gpuKey), ("cloud", cloudKey)] {
                let k = key.trimmingCharacters(in: .whitespacesAndNewlines)
                if !k.isEmpty { _ = try await app.overflow(action: "set-key", args: ["--tier", tier], input: Data(k.utf8)) }
            }
            gpuKey = ""
            cloudKey = ""
            let args = OverflowArguments.providers(hardAlias: hardAlias, dailyLimit: dailyLimit, gpuURL: gpuURL, gpuModel: gpuModel,
                                                   cloudURL: cloudURL, cloudModel: cloudModel, gpuPeers: gpuPeers, cloudPeers: cloudPeers)
            let data = try await app.overflow(action: "set", args: args)
            apply(try JSONDecoder().decode(OverflowStatus.self, from: data))
            message = "Saved."
        } catch { message = "Not saved: \(error.localizedDescription)" }
    }

    /// One click: stores the RunPod key (stdin only), then has the connector create the endpoint and fill in the GPU tier.
    /// Offloading itself stays off until the owner turns it on and chooses which peers may use it.
    func setUpRunPod(_ app: AppModel) async {
        busy = true
        defer { busy = false }
        message = "Setting up the GPU endpoint\u{2026}"
        do {
            let k = gpuKey.trimmingCharacters(in: .whitespacesAndNewlines)
            if !k.isEmpty {
                _ = try await app.overflow(action: "set-key", args: ["--tier", "gpu"], input: Data(k.utf8))
                gpuKey = ""
                gpuKeyStored = true
            }
            guard gpuKeyStored else {
                message = "Paste your RunPod API key first."
                return
            }
            let data = try await app.overflow(action: "provision", args: OverflowArguments.provision(preset: preset, maxWorkers: maxWorkers))
            apply(try JSONDecoder().decode(OverflowStatus.self, from: data))
            message = "Ready. The GPU is configured; turn offloading on under AI inference when you want to use it."
        } catch { message = "Setup failed: \(error.localizedDescription)" }
    }

    /// Deletes the endpoint and its template from the owner's RunPod account and clears the GPU tier.
    func removeRunPod(_ app: AppModel) async {
        busy = true
        defer { busy = false }
        message = "Removing the GPU endpoint\u{2026}"
        do {
            let data = try await app.overflow(action: "deprovision")
            apply(try JSONDecoder().decode(OverflowStatus.self, from: data))
            message = "Removed from your RunPod account."
        } catch { message = "Not removed: \(error.localizedDescription)" }
    }

    /// Asks RunPod whether the endpoint still exists (the owner may have deleted it in RunPod's console).
    func checkRunPod(_ app: AppModel) async {
        busy = true
        defer { busy = false }
        do {
            let data = try await app.overflow(action: "check")
            struct Check: Decodable { var runpodEndpointExists: Bool? }
            let exists = try JSONDecoder().decode(Check.self, from: data).runpodEndpointExists ?? false
            message = exists ? "The endpoint is running normally (it scales to zero when idle)." : "The endpoint is gone from RunPod. Set it up again."
        } catch { message = "Could not check: \(error.localizedDescription)" }
    }

    func removeKey(_ tier: String, _ app: AppModel) async {
        _ = try? await app.overflow(action: "clear-key", args: ["--tier", tier])
        await refresh(app)
    }
}

/// Under AI inference: how traffic from the model running on this Mac is offloaded to the GPU provider.
struct OverflowOffloadView: View {
    @EnvironmentObject private var app: AppModel
    @ObservedObject private var model = OverflowModel.shared

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("OFFLOAD TO A GPU").font(.caption2.weight(.semibold)).tracking(0.6).foregroundStyle(.secondary)
            Text("Send requests this Mac's model cannot handle to a bigger model on a serverless GPU (RunPod). Off by default.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if !model.gpuConfigured {
                Label("Add your RunPod endpoint and API key in Settings \u{203A} AI Offload first.", systemImage: "key")
                    .font(.caption).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            }
            Picker("Offload", selection: $model.mode) {
                ForEach(OverflowMode.allCases) { Text($0.title).tag($0) }
            }
            .labelsHidden()
            .accessibilityIdentifier("overflow-mode")
            Text(model.mode.explanation).font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if model.mode.usesWait || model.mode.usesPrompt {
                HStack {
                    if model.mode.usesWait { limit("Queue wait (seconds)", $model.maxWait) }
                    if model.mode.usesPrompt { limit("Long prompt (tokens)", $model.maxPrompt) }
                }
            }
            if model.mode != .off {
                Label("A listed peer's prompt leaves your network for the GPU provider. Callers see which backend answered.", systemImage: "lock.open")
                    .font(.caption2).foregroundStyle(.orange).fixedSize(horizontal: false, vertical: true)
            }
            HStack {
                Button("Save") { Task { await model.saveOffload(app) } }
                    .buttonStyle(.borderedProminent).controlSize(.small).disabled(model.busy)
                if let m = model.message { Text(m).font(.caption).foregroundStyle(.secondary) }
                Spacer()
            }
        }
        .task { await model.refresh(app) }
    }

    private func limit(_ title: String, _ text: Binding<String>) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title).font(.caption2).foregroundStyle(.secondary)
            TextField("", text: text).textFieldStyle(.roundedBorder).controlSize(.small).frame(maxWidth: 140)
        }
    }
}

/// Settings, AI Offload tab: where the GPU (and optional cloud fallback) is, and its API key.
struct OverflowProvidersView: View {
    @EnvironmentObject private var app: AppModel
    @ObservedObject private var model = OverflowModel.shared

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            oneClickSetup
            GroupBox("Serverless GPU (RunPod), manual") {
                tierFields(url: $model.gpuURL, modelId: $model.gpuModel, key: $model.gpuKey, stored: model.gpuKeyStored,
                           peers: $model.gpuPeers, tier: "gpu", urlHint: "https://api.runpod.ai/v2/<endpoint id>/openai/v1",
                           modelHint: "Qwen/Qwen3-32B")
                    .padding(8)
            }
            GroupBox("Cloud API fallback (optional, used if the GPU is too slow)") {
                tierFields(url: $model.cloudURL, modelId: $model.cloudModel, key: $model.cloudKey, stored: model.cloudKeyStored,
                           peers: $model.cloudPeers, tier: "cloud", urlHint: "https://api.openai.com/v1", modelHint: "gpt-4o-mini")
                    .padding(8)
            }
            GroupBox("Limits") {
                HStack {
                    field("Model name that asks for the GPU", $model.hardAlias, "qwen-large")
                    field("Daily request cap (0: none)", $model.dailyLimit, "200")
                }.padding(8)
            }
            HStack {
                Button("Save") { Task { await model.saveProviders(app) } }
                    .buttonStyle(.borderedProminent).disabled(model.busy)
                if let m = model.message { Text(m).font(.caption).foregroundStyle(.secondary) }
                Spacer()
            }
            Text("API keys are kept in the macOS Keychain, not in the settings file. Choose how traffic is offloaded under AI inference in the neXal panel.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
        .task { await model.refresh(app) }
    }

    @State private var confirmSetup = false
    @State private var confirmRemove = false

    /// Creates the RunPod endpoint for the owner: paste the key, pick a size, click once.
    private var oneClickSetup: some View {
        GroupBox("One-click GPU setup (RunPod)") {
            VStack(alignment: .leading, spacing: 8) {
                if let rp = model.runpod {
                    Label("GPU endpoint ready: \(rp.model)", systemImage: "checkmark.seal")
                        .font(.callout).foregroundStyle(.green)
                    Text("Endpoint \(rp.endpointId). It scales to zero, so it costs nothing while idle; you pay RunPod per second while a GPU works"
                         + (rp.maxWorkers.map { ", at most \($0) at a time." } ?? "."))
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    HStack {
                        Button("Check") { Task { await model.checkRunPod(app) } }
                        Button("Remove endpoint", role: .destructive) { confirmRemove = true }
                        Spacer()
                    }
                    .disabled(model.busy)
                } else {
                    Text("Paste your RunPod API key and click once. neXal creates a serverless GPU endpoint in your RunPod account and fills in the settings below.")
                        .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(model.gpuKeyStored ? "RunPod API key \u{2713} saved in the Keychain (type to replace)" : "RunPod API key")
                            .font(.caption2).foregroundStyle(.secondary)
                        SecureField("Paste your RunPod API key", text: $model.gpuKey).textFieldStyle(.roundedBorder)
                    }
                    HStack(alignment: .bottom) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Size").font(.caption2).foregroundStyle(.secondary)
                            Picker("Size", selection: $model.preset) {
                                ForEach(RunPodPreset.allCases) { Text($0.title).tag($0) }
                            }
                            .labelsHidden()
                        }
                        VStack(alignment: .leading, spacing: 2) {
                            Text("Most GPUs at once (cost cap)").font(.caption2).foregroundStyle(.secondary)
                            TextField("1", text: $model.maxWorkers).textFieldStyle(.roundedBorder).frame(maxWidth: 80)
                        }
                    }
                    HStack {
                        Button("Set up GPU endpoint") { confirmSetup = true }
                            .buttonStyle(.borderedProminent)
                            .disabled(model.busy || (!model.gpuKeyStored && model.gpuKey.trimmingCharacters(in: .whitespaces).isEmpty))
                        Spacer()
                    }
                }
                if let m = model.message { Text(m).font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true) }
                Text("Offloading stays off until you turn it on under AI inference and choose which peers may use it. The first request after the GPU has been idle waits while the model loads.")
                    .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            }
            .padding(8)
            // One alert per view: SwiftUI honors only the last of several alerts on the same view.
            .alert("Remove the GPU endpoint?", isPresented: $confirmRemove) {
                Button("Remove", role: .destructive) { Task { await model.removeRunPod(app) } }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("This deletes the endpoint and its worker template from your RunPod account. Your API key stays saved.")
            }
        }
        .alert("Create a GPU endpoint in your RunPod account?", isPresented: $confirmSetup) {
            Button("Create endpoint") { Task { await model.setUpRunPod(app) } }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("This creates a billable RunPod serverless endpoint. It runs no GPU until a request arrives and stops when idle, with at most \(model.maxWorkers) GPU(s) at once.")
        }
    }

    private func field(_ title: String, _ text: Binding<String>, _ prompt: String) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title).font(.caption2).foregroundStyle(.secondary)
            TextField(prompt, text: text).textFieldStyle(.roundedBorder)
        }
    }

    @ViewBuilder private func tierFields(url: Binding<String>, modelId: Binding<String>, key: Binding<String>, stored: Bool,
                                         peers: Binding<String>, tier: String, urlHint: String, modelHint: String) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            field("Endpoint", url, urlHint)
            field("Model", modelId, modelHint)
            HStack(alignment: .bottom) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(stored ? "API key \u{2713} saved in the Keychain (type to replace)" : "API key").font(.caption2).foregroundStyle(.secondary)
                    SecureField("Paste your API key", text: key).textFieldStyle(.roundedBorder)
                }
                if stored {
                    Button("Remove key") { Task { await model.removeKey(tier, app) } }
                }
            }
            field("Peers allowed (neXal IPs, comma-separated, or * for everyone)", peers, "*")
        }
    }
}
