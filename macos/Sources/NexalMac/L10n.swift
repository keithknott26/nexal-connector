import AppKit
import SwiftUI

/// Localization entry points. SwiftUI literals (`Text("Connected")`, `Label`, `Button`, `Toggle`...)
/// localize on their own from the app bundle's `<lang>.lproj/Localizable.strings`. Use these for
/// strings that are not SwiftUI literals: alerts, computed `String` values, error text.
///
/// The English text is the key; write the user-visible English directly and let
/// `i18n/scripts/l10n.py extract` collect it. Never build a sentence from translated pieces:
/// give the whole sentence a key with `%@` / `%lld` placeholders so translators can reorder it.
enum L10n {
    /// The localized string for `key` (English text) with `args` substituted for `%@`, `%lld`, ...
    static func tr(_ key: String, _ args: CVarArg...) -> String {
        let format = Bundle.main.localizedString(forKey: key, value: key, table: nil)
        return args.isEmpty ? format : String(format: format, locale: Locale.current, arguments: args)
    }
}

/// The app language: follows macOS unless the user picks one here. macOS applies a change at the
/// next launch, so picking offers a restart.
enum AppLanguage {
    private static let choiceKey = "nexal.language"

    /// Languages this build ships, as language codes ("en" first), from the app bundle.
    static var available: [String] {
        let codes = Bundle.main.localizations.filter { $0 != "Base" }
        return codes.sorted { a, b in a == "en" ? true : b == "en" ? false : name(of: a) < name(of: b) }
    }

    /// The user's explicit choice, or nil to follow the system.
    static var choice: String? { UserDefaults.standard.string(forKey: choiceKey) }

    /// A language written in itself ("Español", "日本語").
    static func name(of code: String) -> String {
        let locale = Locale(identifier: code)
        return (locale.localizedString(forIdentifier: code) ?? code).localizedCapitalized
    }

    /// nil means follow the system.
    static func set(_ code: String?) {
        let defaults = UserDefaults.standard
        if let code {
            defaults.set(code, forKey: choiceKey)
            defaults.set([code], forKey: "AppleLanguages")
        } else {
            defaults.removeObject(forKey: choiceKey)
            defaults.removeObject(forKey: "AppleLanguages")
        }
    }

    /// Quits and reopens the app so the new language applies.
    @MainActor static func relaunch() {
        let path = Bundle.main.bundleURL.path
        let helper = Process()
        helper.executableURL = URL(fileURLWithPath: "/bin/sh")
        helper.arguments = ["-c", "sleep 1; /usr/bin/open -n \"$1\"", "sh", path]
        try? helper.run()
        NSApp.terminate(nil)
    }
}

/// Settings group for choosing the language.
struct LanguageSettingsGroup: View {
    @State private var selection: String = AppLanguage.choice ?? ""
    @State private var changed = false

    var body: some View {
        GroupBox("Language") {
            VStack(alignment: .leading, spacing: 10) {
                Picker("App language", selection: $selection) {
                    Text("Same as macOS").tag("")
                    ForEach(AppLanguage.available, id: \.self) { code in
                        Text(AppLanguage.name(of: code)).tag(code)
                    }
                }
                .onChange(of: selection) { _, new in
                    AppLanguage.set(new.isEmpty ? nil : new)
                    changed = true
                }
                Text("Takes effect the next time neXal@home opens.")
                    .font(.caption).foregroundStyle(.secondary)
                if changed {
                    Button("Restart Now") { AppLanguage.relaunch() }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading).padding(4)
        }
    }
}
