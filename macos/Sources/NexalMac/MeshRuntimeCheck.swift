import AppKit
import SwiftUI

/// Every Mac, the gateway and the sidecar must run the same mesh runtime build
/// (experiments/mlkem1024-mesh/RUNTIME_VERSION): a peer on an older "gated.N" cannot complete the
/// ML-KEM-1024 exchange reliably, so its links stay Limited. This compares the installed
/// runtime with the build this app expects.
enum MeshRuntimeCheck {
    /// Keep in step with experiments/mlkem1024-mesh/RUNTIME_VERSION (a test enforces it).
    static let expectedVersion = "0.79.0-nexal-mlkem1024-gated.14"
    static let fixCommand = "bash macos/scripts/update-runtime.sh"

    /// The N in "...gated.N[-suffix]", or nil when the version has no gate number.
    static func gate(from version: String) -> Int? {
        guard let range = version.range(of: "gated.") else { return nil }
        let digits = version[range.upperBound...].prefix { $0.isNumber }
        return Int(digits)
    }

    /// True only when the installed gate number is known and older than expected.
    static func isOutdated(installed: String?, expected: String = expectedVersion) -> Bool {
        guard let installed, let have = gate(from: installed), let want = gate(from: expected) else { return false }
        return have < want
    }

    /// Runs `nexal-network version` from this app bundle. Nil when it is absent or fails.
    nonisolated static func installedVersion(bundle: URL = Bundle.main.bundleURL) -> String? {
        let tool = bundle.appendingPathComponent("Contents/Helpers/nexal-network")
        guard FileManager.default.isExecutableFile(atPath: tool.path) else { return nil }
        let process = Process()
        process.executableURL = tool
        process.arguments = ["version"]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = FileHandle.nullDevice
        do { try process.run() } catch { return nil }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else { return nil }
        let text = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        return text.isEmpty ? nil : text
    }
}

/// Shown only when the installed mesh runtime is older than this app expects.
struct MeshRuntimeBanner: View {
    @State private var installed: String?

    var body: some View {
        Group {
            if MeshRuntimeCheck.isOutdated(installed: installed) {
                VStack(alignment: .leading, spacing: 6) {
                    Label("Mesh runtime out of date", systemImage: "exclamationmark.triangle.fill")
                        .font(.subheadline.weight(.semibold)).foregroundStyle(.orange)
                    Text("This Mac runs \(installed ?? "an older build"); the network expects \(MeshRuntimeCheck.expectedVersion). Links may stay at Limited (no Level 5) until it is updated. In the neXal-connector folder run:")
                        .font(.caption).fixedSize(horizontal: false, vertical: true)
                    HStack {
                        Text(MeshRuntimeCheck.fixCommand)
                            .font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                        Spacer()
                        Button("Copy") {
                            NSPasteboard.general.clearContents()
                            NSPasteboard.general.setString(MeshRuntimeCheck.fixCommand, forType: .string)
                        }.controlSize(.small)
                    }
                }
                .padding(10)
                .background(RoundedRectangle(cornerRadius: 8).fill(Color.orange.opacity(0.12)))
                .accessibilityIdentifier("mesh-runtime-outdated")
            }
        }
        .task { installed = await Task.detached { MeshRuntimeCheck.installedVersion() }.value }
    }
}
