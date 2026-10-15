import XCTest
@testable import NexalMac

final class LocalizationTests: XCTestCase {
    private var resources: URL {
        URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("Resources")
    }

    private func strings(_ code: String) throws -> [String: String] {
        let url = resources.appendingPathComponent("\(code).lproj/Localizable.strings")
        return try XCTUnwrap(NSDictionary(contentsOf: url) as? [String: String], "\(code) must parse as a .strings file")
    }

    func testEveryLanguageParsesAndMatchesEnglishKeys() throws {
        let english = try strings("en")
        XCTAssertFalse(english.isEmpty)
        let folders = try FileManager.default.contentsOfDirectory(atPath: resources.path).filter { $0.hasSuffix(".lproj") }
        for folder in folders {
            let code = String(folder.dropLast(".lproj".count))
            let other = try strings(code)
            XCTAssertEqual(Set(other.keys), Set(english.keys), "\(code) must have exactly the English keys (run i18n/scripts/l10n.py sync)")
        }
    }

    func testTrFormatsArguments() {
        // Falls back to the key itself, so this holds in every language the key is not yet in.
        XCTAssertEqual(L10n.tr("Hello %@, you have %lld peers", "K", 3), "Hello K, you have 3 peers")
        XCTAssertEqual(L10n.tr("No placeholders here"), "No placeholders here")
    }

    func testLanguageNamesAreWrittenInTheirOwnLanguage() {
        XCTAssertEqual(AppLanguage.name(of: "en"), "English")
        XCTAssertEqual(AppLanguage.name(of: "es"), "Español")
        XCTAssertEqual(AppLanguage.name(of: "ja"), "日本語")
    }
}
