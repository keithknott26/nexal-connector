import Foundation

/// The Nexal @ Home versus Nexal @ Work distinction, kept legible in this panel
/// and not only in Finder (HARDENING-PLAN §25.4, required by §26.5).
///
/// The two domains have materially different confidentiality guarantees, so the
/// weaker one is stated with equal prominence to the stronger one. Per §25.4 the
/// word "Nexal" never appears unqualified in this copy: every mention names the
/// domain, because an unqualified claim would collapse two different guarantees
/// into one.
struct DomainEscrowPresentation: Equatable {
    static let title = "Who can read your files"

    /// Both guarantees, stated together. Neither sentence may be shown without
    /// the other.
    static let text = """
        Nexal @ Home: nobody but you can read it. Nexal @ Work: your company's \
        administrators can recover its contents, and can therefore read them. \
        Moving a file from Nexal @ Home into Nexal @ Work changes who can read \
        that file. This build enrolls privately and reports no domain of its own \
        yet, so nothing here tells you which one a future shared folder belongs \
        to — the folders themselves do.
        """

    /// Every mention of the product name in this copy is qualified by a domain.
    static var namesAreQualified: Bool {
        var remainder = Substring(text)
        while let found = remainder.range(of: "Nexal") {
            let tail = remainder[found.upperBound...]
            guard tail.hasPrefix(" @ Home") || tail.hasPrefix(" @ Work") else { return false }
            remainder = tail
        }
        return true
    }
}
