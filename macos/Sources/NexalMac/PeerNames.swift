import AppKit
import SwiftUI

/// Names people give peers of the secure network (a Mac, an iPhone, the storage gateway, a VM or
/// dev container), kept by the coordinator per mesh address and shared with the iPhone app.
/// Read and written through the connector (`nexal peer-names`), which holds the host token.
@MainActor
final class PeerNames: ObservableObject {
    static let shared = PeerNames()
    static let maxLength = 60

    @Published private(set) var names: [String: String] = [:]
    private var lastFetch = Date.distantPast

    private struct Reply: Decodable {
        struct Entry: Decodable { let address: String; let name: String }
        let names: [Entry]?
    }

    /// The name given to the peer at this mesh address, if any.
    func name(for address: String?) -> String? {
        guard let address, let n = names[address], !n.isEmpty else { return nil }
        return n
    }

    func refresh(_ model: AppModel, force: Bool = false) async {
        guard force || Date().timeIntervalSince(lastFetch) > 30 else { return }
        lastFetch = Date()
        // An older connector or coordinator without peer names simply yields none.
        guard let data = try? await model.peerNames(action: "list"),
              let reply = try? JSONDecoder().decode(Reply.self, from: data) else { return }
        names = Dictionary((reply.names ?? []).map { ($0.address, $0.name) }, uniquingKeysWith: { _, last in last })
    }

    func save(address: String, name: String, model: AppModel) async {
        let clean = String(name.trimmingCharacters(in: .whitespacesAndNewlines).prefix(Self.maxLength))
        do {
            _ = try await model.peerNames(action: "set", address: address, input: Data((clean + "\n").utf8))
            if clean.isEmpty { names[address] = nil } else { names[address] = clean }
            await refresh(model, force: true) // what the coordinator stored, for every peer
            await ThrowawayHosting.shared.refreshNetwork(model, force: true)
        } catch {
            Self.report("Could not rename", error.localizedDescription)
        }
    }

    static func report(_ title: String, _ detail: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = detail
        alert.window.level = .floating
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
    }

    static func validAddress(_ s: String) -> Bool {
        let parts = s.split(separator: ".", omittingEmptySubsequences: false).compactMap { Int($0) }
        return parts.count == 4 && parts.allSatisfy { (0...255).contains($0) } && parts[0] == 100 && (64...127).contains(parts[1])
    }

}

/// Double-click the name to edit it in place (Return saves, Esc cancels), or right-click › Rename….
/// The name is saved by the coordinator, which also renames the peer on the network (its DNS
/// name), so the change shows on every Mac and iPhone.
struct RenameOnInteraction: ViewModifier {
    let address: String?
    let current: String
    @EnvironmentObject private var model: AppModel
    @State private var editing = false
    @State private var draft = ""
    @State private var saving = false
    @FocusState private var focused: Bool

    func body(content: Content) -> some View {
        Group {
            if editing {
                HStack(spacing: 4) {
                    TextField("Name", text: $draft)
                        .textFieldStyle(.roundedBorder)
                        .font(.callout)
                        .frame(minWidth: 140, maxWidth: 240)
                        .focused($focused)
                        .onSubmit { commit() }
                        .onExitCommand { editing = false }
                        .onChange(of: focused) { _, isFocused in if !isFocused && !saving { editing = false } }
                        .onChange(of: draft) { _, value in
                            if value.count > PeerNames.maxLength { draft = String(value.prefix(PeerNames.maxLength)) }
                        }
                    if saving { ProgressView().controlSize(.mini) }
                }
            } else {
                content
                    .onTapGesture(count: 2) { begin() }
                    .help("Double-click to rename")
            }
        }
        .contextMenu {
            Button("Rename…") { begin() }
            if address != nil, PeerNames.shared.name(for: address) != nil {
                Button("Use Original Name") { save("") }
            }
        }
    }

    private func begin() {
        guard let address, PeerNames.validAddress(address) else {
            PeerNames.report("Cannot rename yet", "This peer has no secure-network address yet; try again once it is connected.")
            return
        }
        draft = current
        editing = true
        DispatchQueue.main.async { focused = true }
    }

    private func commit() {
        let name = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard name != current else { editing = false; return } // nothing to change: no request at all
        save(name)
    }

    private func save(_ name: String) {
        guard let address else { return }
        saving = true
        Task {
            await PeerNames.shared.save(address: address, name: name, model: model)
            await model.refresh()
            saving = false
            editing = false
        }
    }
}

extension View {
    func renamable(address: String?, current: String) -> some View {
        modifier(RenameOnInteraction(address: address, current: current))
    }
}
