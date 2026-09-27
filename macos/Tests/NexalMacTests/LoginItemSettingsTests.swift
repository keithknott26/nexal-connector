import ServiceManagement
import XCTest
@testable import NexalMac

final class LoginItemSettingsTests: XCTestCase {
    @MainActor func testDefaultRegistersOnceAndRespectsExternalDisable() {
        let name = "LoginItemSettingsTests.\(UUID())"
        let defaults = UserDefaults(suiteName: name)!
        defer { defaults.removePersistentDomain(forName: name) }
        var status = SMAppService.Status.notRegistered
        var registrations = 0
        let settings = LoginItemSettings(defaults: defaults, status: { status }, register: {
            registrations += 1
            status = .enabled
        }, unregister: { status = .notRegistered })
        settings.configureFirstLaunch()
        XCTAssertTrue(settings.startsAtLogin)
        XCTAssertEqual(registrations, 1)
        status = .notRegistered // Owner disables it in macOS System Settings.
        settings.configureFirstLaunch()
        XCTAssertFalse(settings.startsAtLogin)
        XCTAssertEqual(registrations, 1)
    }

    @MainActor func testOptOutSurvivesRelaunchAndCanBeEnabledAgain() {
        let name = "LoginItemSettingsTests.\(UUID())"
        let defaults = UserDefaults(suiteName: name)!
        defer { defaults.removePersistentDomain(forName: name) }
        var status = SMAppService.Status.notRegistered
        let settings = LoginItemSettings(defaults: defaults, status: { status },
                                         register: { status = .enabled }, unregister: { status = .notRegistered })
        settings.configureFirstLaunch()
        settings.setStartsAtLogin(false)
        let relaunched = LoginItemSettings(defaults: defaults, status: { status },
                                           register: { status = .enabled }, unregister: { status = .notRegistered })
        relaunched.configureFirstLaunch()
        XCTAssertFalse(relaunched.startsAtLogin)
        relaunched.setStartsAtLogin(true)
        XCTAssertTrue(relaunched.startsAtLogin)
    }

    @MainActor func testApprovalAndFailuresReflectActualSystemState() {
        let name = "LoginItemSettingsTests.\(UUID())"
        let defaults = UserDefaults(suiteName: name)!
        defer { defaults.removePersistentDomain(forName: name) }
        var status = SMAppService.Status.notRegistered
        var fail = true
        let settings = LoginItemSettings(defaults: defaults, status: { status }, register: {
            if fail { throw NSError(domain: "test", code: 1) }
            status = .requiresApproval
        }, unregister: { throw NSError(domain: "test", code: 2) })
        settings.configureFirstLaunch()
        XCTAssertFalse(settings.startsAtLogin)
        XCTAssertNotNil(settings.errorMessage)
        fail = false
        settings.configureFirstLaunch()
        XCTAssertTrue(settings.startsAtLogin)
        XCTAssertTrue(settings.requiresApproval)
        XCTAssertNil(settings.errorMessage)
        settings.setStartsAtLogin(false)
        XCTAssertTrue(settings.startsAtLogin)
        XCTAssertNotNil(settings.errorMessage)
    }
}
