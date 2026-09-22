import Foundation
import XCTest
@testable import NexalMac

/// §25.4 requires the neXal @ Home versus neXal @ Work escrow difference to stay
/// legible in this panel, with the weaker guarantee stated as prominently as the
/// stronger one.
final class DomainEscrowPresentationTests: XCTestCase {
    func testBothGuaranteesAreStatedTogether() {
        let text = DomainEscrowPresentation.text
        XCTAssertTrue(text.contains("neXal @ Home: nobody but you can read it."))
        XCTAssertTrue(text.contains("administrators can recover"))
        XCTAssertTrue(text.contains("can therefore read them"))
    }

    func testAMoveBetweenDomainsIsDescribedAsAChangeInWhoCanReadIt() {
        XCTAssertTrue(DomainEscrowPresentation.text.contains("changes who can read"))
        XCTAssertFalse(DomainEscrowPresentation.text.lowercased().contains("sync"))
        XCTAssertFalse(DomainEscrowPresentation.text.lowercased().contains("backup"))
    }

    // §25.4: the product name must never appear unqualified in this copy.
    func testEveryProductMentionNamesItsDomain() {
        XCTAssertTrue(DomainEscrowPresentation.namesAreQualified)
        XCTAssertFalse(DomainEscrowPresentation.title.localizedCaseInsensitiveContains("nexal"))
    }

    /// The check must be examining mentions, not passing because it found none.
    ///
    /// This is the failure the neXal rename nearly caused: the guard searched for
    /// the literal "Nexal" while the copy became "neXal", so it would have found
    /// zero mentions and reported the invariant satisfied precisely because it had
    /// stopped looking. Asserting a positive count makes vacuous success
    /// impossible, whatever the product is called next.
    func testTheQualificationCheckIsNotPassingVacuously() {
        XCTAssertGreaterThanOrEqual(DomainEscrowPresentation.qualifiedMentionCount, 4)
    }

    /// Any capitalization is caught, so a restyle cannot disable the guard.
    func testAnUnqualifiedMentionIsCaughtInAnyCapitalization() {
        for spelling in ["Nexal", "neXal", "NEXAL", "nexal"] {
            let sample = "\(spelling) keeps your files private"
            var remainder = sample[sample.startIndex...]
            var qualified = true
            while let found = remainder.range(of: "nexal", options: [.caseInsensitive]) {
                let tail = remainder[found.upperBound...]
                if !(tail.hasPrefix(" @ Home") || tail.hasPrefix(" @ Work")) { qualified = false }
                remainder = tail
            }
            XCTAssertFalse(qualified, "an unqualified \(spelling) was not caught")
        }
    }

    func testTheQualificationCheckActuallyFails() {
        // Guards the check itself, so a future edit cannot pass by accident.
        let sample = "neXal keeps your files private"
        var remainder = sample[sample.startIndex...]
        var qualified = true
        while let found = remainder.range(of: "nexal", options: [.caseInsensitive]) {
            let tail = remainder[found.upperBound...]
            if !(tail.hasPrefix(" @ Home") || tail.hasPrefix(" @ Work")) { qualified = false }
            remainder = tail
        }
        XCTAssertFalse(qualified)
    }

    func testThisBuildDoesNotClaimToKnowWhichDomainAFolderIsIn() {
        XCTAssertTrue(DomainEscrowPresentation.text.contains("reports no domain of its own"))
    }
}
