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

    /// Asks for a new name and saves it. An empty name goes back to the peer's own.
    func rename(address: String?, current: String, model: AppModel) {
        guard let address, Self.validAddress(address) else {
            Self.report("Cannot rename yet", "This peer has no secure-network address yet; try again once it is connected.")
            return
        }
        guard let entered = Self.prompt(current: current) else { return }
        Task { await save(address: address, name: entered, model: model) }
    }

    func save(address: String, name: String, model: AppModel) async {
        let clean = String(name.trimmingCharacters(in: .whitespacesAndNewlines).prefix(Self.maxLength))
        do {
            _ = try await model.peerNames(action: "set", address: address, input: Data((clean + "\n").utf8))
            if clean.isEmpty { names[address] = nil } else { names[address] = clean }
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

    /// A small modal prompt. NSAlert works from the menu-bar panel, where SwiftUI alerts are unreliable.
    static func prompt(current: String) -> String? {
        let alert = NSAlert()
        alert.messageText = "Rename “\(current)”"
        alert.informativeText = "Shown on your Macs and iPhone. Leave empty to use its own name again."
        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 260, height: 24))
        field.stringValue = current
        field.placeholderString = "Name"
        alert.accessoryView = field
        alert.addButton(withTitle: "Rename")
        alert.addButton(withTitle: "Cancel")
        alert.window.initialFirstResponder = field
        alert.window.level = .floating
        alert.layout()
        NSApp.activate(ignoringOtherApps: true)
        guard alert.runModal() == .alertFirstButtonReturn else { return nil }
        return field.stringValue
    }
}

/// Right-click › Rename…, or double-click the name.
struct RenameOnInteraction: ViewModifier {
    let address: String?
    let current: String
    @EnvironmentObject private var model: AppModel

    func body(content: Content) -> some View {
        content
            .onTapGesture(count: 2) { PeerNames.shared.rename(address: address, current: current, model: model) }
            .contextMenu {
                Button("Rename…") { PeerNames.shared.rename(address: address, current: current, model: model) }
            }
            .help("Double-click or right-click to rename")
    }
}

extension View {
    func renamable(address: String?, current: String) -> some View {
        modifier(RenameOnInteraction(address: address, current: current))
    }
}
