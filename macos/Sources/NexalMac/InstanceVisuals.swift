import AppKit
import SwiftUI

/// The OS or app an instance runs, for its icon: "ubuntu", "debian", "fedora", "alpine",
/// "home-assistant", "devcontainer" or "linux".
enum InstanceFamily {
    static func of(imageId: String?, imageName: String?, appProfile: String?, template: String?) -> String {
        if let appProfile, appProfile != "none" { return appProfile }
        let text = [template, imageId, imageName].compactMap { $0?.lowercased() }.joined(separator: " ")
        for known in ["ubuntu", "debian", "fedora", "alpine", "home-assistant", "jellyfin", "rocky", "arch"] where text.contains(known) { return known }
        if text.contains("home assistant") { return "home-assistant" }
        if text.contains("devcontainer") || text.contains("dev container") { return "devcontainer" }
        return "linux"
    }
}

/// A small badge for an instance's OS. If the app bundle carries an image named
/// `os-<family>` (an official logo added by the developer), that is shown; otherwise
/// a generic symbol in the family's color.
struct InstanceIcon: View {
    let family: String
    var size: CGFloat = 16

    private var corner: CGFloat { size * 0.24 }

    var body: some View {
        Group {
            if let logo = NSImage(named: "os-\(family)") {
                // Official logos sit on a light tile so every mark (including thin ones) reads at small sizes.
                Image(nsImage: logo).resizable().interpolation(.high).scaledToFit()
                    .padding(size * 0.14)
                    .frame(width: size, height: size)
                    .background(RoundedRectangle(cornerRadius: corner, style: .continuous).fill(Color.white))
            } else {
                Image(systemName: Self.symbol(family))
                    .font(.system(size: size * 0.5, weight: .semibold))
                    .foregroundStyle(.white)
                    .frame(width: size, height: size)
                    .background(RoundedRectangle(cornerRadius: corner, style: .continuous).fill(Self.tint(family).gradient))
            }
        }
        .overlay(RoundedRectangle(cornerRadius: corner, style: .continuous).strokeBorder(.black.opacity(0.08)))
        .accessibilityLabel(Self.label(family))
    }

    /// A 16 pt image for menus and pickers: the official logo when bundled, else the symbol.
    static func menuImage(_ family: String) -> Image {
        if let logo = NSImage(named: "os-\(family)")?.copy() as? NSImage {
            logo.size = NSSize(width: 16, height: 16)
            return Image(nsImage: logo)
        }
        return Image(systemName: symbol(family))
    }

    static func symbol(_ family: String) -> String {
        switch family {
        case "home-assistant": return "house.fill"
        case "jellyfin": return "play.tv.fill"
        case "devcontainer": return "shippingbox.fill"
        case "ubuntu", "debian", "fedora", "alpine", "rocky", "arch": return "server.rack"
        default: return "terminal.fill"
        }
    }
    static func tint(_ family: String) -> Color {
        switch family {
        case "ubuntu": return .orange
        case "debian": return .red
        case "fedora", "home-assistant": return .blue
        case "jellyfin": return .purple
        case "alpine": return .teal
        case "rocky": return .green
        case "arch": return .cyan
        case "devcontainer": return .indigo
        default: return .gray
        }
    }
    static func label(_ family: String) -> String {
        switch family {
        case "home-assistant": return "Home Assistant"
        case "jellyfin": return "Jellyfin"
        case "devcontainer": return "Dev container"
        case "linux": return "Linux"
        default: return family.capitalized
        }
    }
}

/// The start-up or shut-down step an instance is on, with a small bar.
struct InstanceProgress: View {
    let kind: String?
    let state: String?
    let step: String?
    let percent: Int?
    var appTitle: String? = nil
    var runnerName: String? = nil
    var runnerOnline: Bool? = nil

    private var noun: String { kind == "devcontainer" ? "development container" : "VM" }

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            if let fraction {
                ProgressView(value: fraction).progressViewStyle(.linear).controlSize(.small)
            } else {
                ProgressView().progressViewStyle(.linear).controlSize(.small)
            }
            Text(text).font(.caption2).foregroundStyle(.secondary)
        }
        .accessibilityElement(children: .combine)
    }

    private var fraction: Double? {
        guard state != "stopping", step != "app-failed", let percent else { return nil }
        return min(max(Double(percent) / 100, 0.02), 1)
    }

    var text: String {
        if state == "stopping" { return "Shutting down and removing the \(noun) from the network…" }
        if state == "requested" && step == nil {
            let who = runnerName ?? "the computer"
            if runnerOnline == false { return "Queued: waiting for \(who), which is offline or asleep. It starts when \(who) is back." }
            return "Queued: waiting for \(who) to start it…"
        }
        let app = appTitle ?? "the app"
        switch step {
        case "app-packages": return "Installing \(app): system packages (\(percent ?? 0)%)…"
        case "app-download": return "Installing \(app): downloading \(app) (\(percent ?? 0)%)…"
        case "app-start": return "Installing \(app): starting it (\(percent ?? 0)%)…"
        case "app-failed": return "\(app) setup failed inside the VM. Connect with Terminal to check, or delete and create it again."
        default: break
        }
        switch step {
        case "check": return "Checking the computer…"
        case "download": return "Downloading the system image (\(percent ?? 0)%)…"
        case "convert": return "Preparing the image…"
        case "disk": return "Creating the disk…"
        case "seed": return "Preparing the first boot…"
        case "boot": return kind == "devcontainer" ? "Starting the container… (the first one can take a few minutes)" : "Starting the VM…"
        case "join": return "Adding the \(noun) to the network (setting up secure networking)…"
        default: return "Starting…"
        }
    }

    /// Whether an instance in this state shows progress.
    static func shows(_ state: String?) -> Bool { ["requested", "provisioning", "stopping"].contains(state ?? "") }
}

/// The web app an appliance VM publishes on its mesh address.
struct InstanceApp {
    let title: String
    let port: Int
    init?(profile: String?) {
        switch profile {
        case "home-assistant": title = "Home Assistant"; port = 8123
        case "jellyfin": title = "Jellyfin"; port = 8096
        default: return nil
        }
    }
}
