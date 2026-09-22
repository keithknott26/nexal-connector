import Foundation

/// The neXal @ Home versus neXal @ Work distinction, kept legible in this panel
/// and not only in Finder (HARDENING-PLAN §25.4, required by §26.5).
///
/// The two domains have materially different confidentiality guarantees, so the
/// weaker one is stated with equal prominence to the stronger one. Per §25.4 the
/// word "neXal" never appears unqualified in this copy: every mention names the
/// domain, because an unqualified claim would collapse two different guarantees
/// into one.
struct DomainEscrowPresentation: Equatable {
    static let title = "Who can read your files"

    /// Both guarantees, stated together. Neither sentence may be shown without
    /// the other.
    static let text = """
        neXal @ Home: nobody but you can read it. neXal @ Work: your company's \
        administrators can recover its contents, and can therefore read them. \
        Moving a file from neXal @ Home into neXal @ Work changes who can read \
        that file. This build enrolls privately and reports no domain of its own \
        yet, so nothing here tells you which one a future shared folder belongs \
        to — the folders themselves do.
        """

    /// Every mention of the product name in this copy is qualified by a domain.
    ///
    /// The search is CASE-INSENSITIVE, which matters more than it looks. This
    /// check used to scan for the literal "Nexal"; the rename to "neXal" moved the
    /// copy out from under it, and had the literal not been renamed in lockstep
    /// the guard would have found zero mentions and returned true vacuously --
    /// reporting the §25.4 invariant as satisfied precisely because it had stopped
    /// looking. Matching any capitalization means a future restyle cannot silently
    /// disable the check, and an unqualified "NEXAL" or "nexal" is caught too.
    static var namesAreQualified: Bool {
        var remainder = text[text.startIndex...]
        while let found = remainder.range(of: "nexal", options: [.caseInsensitive]) {
            let tail = remainder[found.upperBound...]
            guard tail.hasPrefix(" @ Home") || tail.hasPrefix(" @ Work") else { return false }
            remainder = tail
        }
        return true
    }

    /// How many product mentions the qualification check actually examined.
    ///
    /// Exposed so a test can assert the check is not passing vacuously. A copy
    /// edit that removed every mention would satisfy namesAreQualified while
    /// saying nothing about the domains at all.
    static var qualifiedMentionCount: Int {
        var count = 0
        var remainder = text[text.startIndex...]
        while let found = remainder.range(of: "nexal", options: [.caseInsensitive]) {
            count += 1
            remainder = remainder[found.upperBound...]
        }
        return count
    }
}
