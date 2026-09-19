import Foundation

/// Presentation metadata only. Never accepts or stores an invitation code.
struct EnrollmentPresentation {
    private var confirmedConfigurations: Set<String> = []
    private var replacementConfigurations: Set<String> = []

    func showsConfirmation(for config: URL) -> Bool {
        let key = config.standardizedFileURL.path
        return confirmedConfigurations.contains(key) && !replacementConfigurations.contains(key)
    }

    mutating func recordSuccess(for config: URL) {
        let key = config.standardizedFileURL.path
        confirmedConfigurations.insert(key)
        replacementConfigurations.remove(key)
    }

    mutating func observeHost(_ hostID: String?, for config: URL) {
        guard let hostID, !hostID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        // A status refresh must not close an explicitly opened replacement form.
        confirmedConfigurations.insert(config.standardizedFileURL.path)
    }

    mutating func beginReplacement(for config: URL) {
        replacementConfigurations.insert(config.standardizedFileURL.path)
    }
}
