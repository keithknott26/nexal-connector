import Foundation

/// UI feedback only. Go remains authoritative for consent, expiry and admission.
struct ManualAcceptancePresentation {
    let activeUntil: Date?

    init(status: ConnectorStatus?, now: Date = Date()) {
        guard let status, !status.paused,
              status.manualAcceptanceSupported == true,
              status.ownerActivityOverride == true,
              let raw = status.acceptJobsUntil else {
            activeUntil = nil
            return
        }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        var expiry = formatter.date(from: raw)
        if expiry == nil {
            formatter.formatOptions = [.withInternetDateTime]
            expiry = formatter.date(from: raw)
        }
        activeUntil = expiry.flatMap { $0 > now ? $0 : nil }
    }

    var isActive: Bool { activeUntil != nil }
    var buttonTitle: String { isActive ? "Accepting private jobs" : "Accept jobs now" }

    static func unavailableReason(developmentEnvironment: Bool, hasExecutable: Bool,
                                  configurationExists: Bool, status: ConnectorStatus?) -> String? {
        if !developmentEnvironment { return "Select the development environment to accept private jobs." }
        if !hasExecutable { return "Choose the updated bundled connector in Setup first." }
        if !configurationExists { return "Create and enroll the development configuration first." }
        if let status, status.hostId?.isEmpty != false {
            return "Enroll this connector before accepting jobs."
        }
        if let status, status.manualAcceptanceSupported != true {
            return "This running connector does not support manual acceptance. Quit the old connector and use the updated app."
        }
        return nil
    }
}
