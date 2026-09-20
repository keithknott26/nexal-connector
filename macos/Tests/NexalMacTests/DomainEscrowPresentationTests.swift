import Foundation
import XCTest
@testable import NexalMac

/// §25.4 requires the Nexal @ Home versus Nexal @ Work escrow difference to stay
/// legible in this panel, with the weaker guarantee stated as prominently as the
/// stronger one.
final class DomainEscrowPresentationTests: XCTestCase {
    func testBothGuaranteesAreStatedTogether() {
        let text = DomainEscrowPresentation.text
        XCTAssertTrue(text.contains("Nexal @ Home: nobody but you can read it."))
        XCTAssertTrue(text.contains("administrators can recover"))
        XCTAssertTrue(text.contains("can therefore read them"))
    }

    func testAMoveBetweenDomainsIsDescribedAsAChangeInWhoCanReadIt() {
        XCTAssertTrue(DomainEscrowPresentation.text.contains("changes who can read"))
        XCTAssertFalse(DomainEscrowPresentation.text.lowercased().contains("sync"))
        XCTAssertFalse(DomainEscrowPresentation.text.lowercased().contains("backup"))
    }

    // §25.4: the word "Nexal" must never appear unqualified in this copy.
    func testEveryProductMentionNamesItsDomain() {
        XCTAssertTrue(DomainEscrowPresentation.namesAreQualified)
        XCTAssertFalse(DomainEscrowPresentation.title.contains("Nexal"))
    }

    func testTheQualificationCheckActuallyFails() {
        // Guards the check itself, so a future edit cannot pass by accident.
        var remainder = Substring("Nexal keeps your files private")
        var qualified = true
        while let found = remainder.range(of: "Nexal") {
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
