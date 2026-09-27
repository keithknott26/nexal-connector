import Foundation
import Combine

/// Local presentation preferences shared by Settings, the panel and the app delegate.
@MainActor
final class ConnectorPreferences: ObservableObject {
    private let defaults: UserDefaults

    @Published var showSplash: Bool {
        didSet { defaults.set(showSplash, forKey: "connectorShowSplash") }
    }
    @Published var playPairingSound: Bool {
        didSet { defaults.set(playPairingSound, forKey: "connectorPlayPairingSound") }
    }
    @Published var flashAlerts: Bool {
        didSet { defaults.set(flashAlerts, forKey: "connectorFlashAlerts") }
    }
    @Published var chartWindowMinutes: Int {
        didSet { defaults.set(chartWindowMinutes, forKey: "connectorChartWindowMinutes") }
    }

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        showSplash = defaults.object(forKey: "connectorShowSplash") as? Bool ?? true
        playPairingSound = defaults.object(forKey: "connectorPlayPairingSound") as? Bool ?? true
        flashAlerts = defaults.object(forKey: "connectorFlashAlerts") as? Bool ?? true
        let minutes = defaults.integer(forKey: "connectorChartWindowMinutes")
        chartWindowMinutes = [5, 30].contains(minutes) ? minutes : 30
    }
}
