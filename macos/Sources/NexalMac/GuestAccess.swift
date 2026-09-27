import Foundation
import SwiftUI

struct GuestAccessRecord: Decodable, Equatable {
    let grantId: String
    let accessExpiresAt: String
    let receivedAt: String
    let inviterEmail: String
    let expired: Bool
    var deadline: Date? { Self.parse(accessExpiresAt) }
    func isExpired(at now: Date = Date()) -> Bool {
        guard !expired, let end = deadline, let start = Self.parse(receivedAt), end > start,
              end.timeIntervalSince(start) <= 3600 else { return true }
        return now >= end || now < start.addingTimeInterval(-5)
    }
    static func parse(_ raw: String) -> Date? {
        let precise = ISO8601DateFormatter(); precise.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return precise.date(from: raw) ?? ISO8601DateFormatter().date(from: raw)
    }
    static func read(at url: URL) -> GuestAccessRecord? {
        struct Saved: Decodable { let guestAccess: GuestAccessRecord? }
        guard let data = try? Data(contentsOf: url), data.count <= 65536 else { return nil }
        return (try? JSONDecoder().decode(Saved.self, from: data))?.guestAccess
    }
}

struct GuestAccessResponse: Decodable {
    let status: String
    let guestAccess: GuestAccessRecord?
    let message: String?
}

struct GuestInvitationEntry: View {
    @EnvironmentObject var model: AppModel
    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let guest = model.guestAccess, guest.isExpired() {
                Label("Your temporary network access has expired", systemImage: "clock.badge.exclamationmark")
                    .font(.headline).foregroundStyle(.orange)
                Text("Ask \(guest.inviterEmail) for a new invitation code to reconnect.")
                    .font(.caption).textSelection(.enabled)
            }
            Text("Already have an invite or pairing code?").font(.headline)
            Text("Enter the invitation code you received. Access lasts one hour from redemption. A phone pairing code must be entered in neXal@home on your iPhone.")
                .font(.caption).foregroundStyle(.secondary)
            TextField("Invitation code", text: $model.guestInvitationCode)
                .textFieldStyle(.roundedBorder).autocorrectionDisabled()
                .accessibilityIdentifier("guest-invitation-code")
            Button("Join with invitation") { Task { await model.redeemGuestInvitation() } }
                .buttonStyle(.bordered).disabled(model.busy || model.guestInvitationCode.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            if let problem = model.guestInvitationProblem {
                Text(problem).font(.caption).foregroundStyle(.orange).textSelection(.enabled)
            }
        }
        .padding(12).background(.secondary.opacity(0.07), in: RoundedRectangle(cornerRadius: 10))
    }
}
