import Foundation

/// Failure while reading, validating or building a VM spec. `description` is the
/// one-line message printed on stderr.
struct SpecError: Error, CustomStringConvertible {
    let description: String
    init(_ message: String) { description = message }
}

struct SharedDirSpec: Decodable, Equatable {
    var tag: String
    var path: String
    var readOnly: Bool?
}

/// Mirrors `sandbox.Spec` in connector/internal/sandbox/vm.go. The last four
/// fields are optional extensions the Go side does not send today.
struct VMSpec: Decodable, Equatable {
    var sandboxId: String
    var hostname: String
    var cpus: Int
    var memoryMB: Int
    var diskPath: String
    var seedPath: String?
    var consoleLog: String
    var controlSocket: String
    var guestSocket: String?
    var desktop: Bool?
    var keepAwake: Bool?
    // Optional extensions.
    var macAddress: String?
    var stopGraceSeconds: Int?
    var sharedDirs: [SharedDirSpec]?
    // A second NIC bridged onto the Mac's LAN through the nexal-vmnet root helper
    // (LANBridge.swift); absent: NAT only.
    var lanSocket: String?
    var lanMacAddress: String?

    static let defaultGraceSeconds = 30

    var seed: String? { (seedPath?.isEmpty ?? true) ? nil : seedPath }
    var guest: String? { (guestSocket?.isEmpty ?? true) ? nil : guestSocket }
    var lan: String? { (lanSocket?.isEmpty ?? true) ? nil : lanSocket }
    var wantsDesktop: Bool { desktop ?? false }
    var graceSeconds: Int { stopGraceSeconds ?? VMSpec.defaultGraceSeconds }

    static func decode(from data: Data) throws -> VMSpec {
        do {
            return try JSONDecoder().decode(VMSpec.self, from: data)
        } catch {
            throw SpecError("invalid spec JSON: \(error.localizedDescription)")
        }
    }

    static func load(path: String) throws -> VMSpec {
        guard let data = FileManager.default.contents(atPath: path) else {
            throw SpecError("cannot read spec file \(path)")
        }
        return try decode(from: data)
    }
}

enum SpecValidator {
    /// unix socket paths are limited to 104 bytes on macOS (including NUL).
    static let maxSocketPath = 103

    static func validate(_ s: VMSpec) throws {
        if s.sandboxId.isEmpty || s.sandboxId.contains("/") { throw SpecError("invalid sandboxId") }
        if s.cpus < 1 { throw SpecError("cpus must be at least 1") }
        if s.memoryMB < 256 { throw SpecError("memoryMB must be at least 256") }
        if let g = s.stopGraceSeconds, !(1...3600).contains(g) {
            throw SpecError("stopGraceSeconds must be 1...3600")
        }
        if let mac = s.macAddress, !mac.isEmpty, !isValidMAC(mac) { throw SpecError("invalid macAddress") }
        if let mac = s.lanMacAddress, !mac.isEmpty, !isValidMAC(mac) { throw SpecError("invalid lanMacAddress") }
        if let lan = s.lan {
            try checkShape(lan, label: "lanSocket")
            if lan.utf8.count > maxSocketPath { throw SpecError("lanSocket is too long for a unix socket") }
        }

        try requirePath(s.diskPath, label: "diskPath", kind: .file)
        if let seed = s.seed { try requirePath(seed, label: "seedPath", kind: .file) }
        try requireWritableTarget(s.consoleLog, label: "consoleLog")
        try requireWritableTarget(s.controlSocket, label: "controlSocket", socket: true)
        if let g = s.guest {
            try requireWritableTarget(g, label: "guestSocket", socket: true)
            if g == s.controlSocket { throw SpecError("guestSocket and controlSocket must differ") }
        }
        var tags = Set<String>()
        for d in s.sharedDirs ?? [] {
            if d.tag.isEmpty || d.tag.contains("/") { throw SpecError("invalid shared directory tag") }
            if !tags.insert(d.tag).inserted { throw SpecError("duplicate shared directory tag \(d.tag)") }
            try requirePath(d.path, label: "sharedDirs[\(d.tag)]", kind: .directory)
        }
    }

    static func isValidMAC(_ s: String) -> Bool {
        s.range(of: "^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$", options: .regularExpression) != nil
    }

    enum Kind { case file, directory }

    private static func checkShape(_ path: String, label: String) throws {
        guard path.hasPrefix("/") else { throw SpecError("\(label) must be an absolute path") }
        if path.contains("\0") { throw SpecError("\(label) contains a NUL byte") }
        if path.split(separator: "/").contains("..") { throw SpecError("\(label) must not contain '..'") }
    }

    /// The final component must exist, have the right type and not be a symlink.
    /// (Parent directories may be symlinks such as /var -> /private/var; a
    /// symlinked final component is the escape we refuse.)
    private static func requirePath(_ path: String, label: String, kind: Kind) throws {
        try checkShape(path, label: label)
        var st = stat()
        guard lstat(path, &st) == 0 else { throw SpecError("\(label) does not exist: \(path)") }
        let type = st.st_mode & S_IFMT
        if type == S_IFLNK { throw SpecError("\(label) is a symlink, refusing: \(path)") }
        switch kind {
        case .file: if type != S_IFREG { throw SpecError("\(label) is not a regular file: \(path)") }
        case .directory: if type != S_IFDIR { throw SpecError("\(label) is not a directory: \(path)") }
        }
    }

    /// A file we will create or replace: the parent directory must exist and the
    /// final component, if present, must not be a symlink.
    private static func requireWritableTarget(_ path: String, label: String, socket: Bool = false) throws {
        try checkShape(path, label: label)
        if socket && path.utf8.count > maxSocketPath { throw SpecError("\(label) is too long for a unix socket") }
        let parent = (path as NSString).deletingLastPathComponent
        var pst = stat()
        guard stat(parent, &pst) == 0, (pst.st_mode & S_IFMT) == S_IFDIR else {
            throw SpecError("\(label) parent directory does not exist: \(parent)")
        }
        var st = stat()
        if lstat(path, &st) == 0, (st.st_mode & S_IFMT) == S_IFLNK {
            throw SpecError("\(label) is a symlink, refusing: \(path)")
        }
    }
}
