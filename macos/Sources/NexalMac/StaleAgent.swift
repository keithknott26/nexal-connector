import Darwin
import Foundation

/// Finds a connector agent (`nexal run`) left running by an earlier install.
///
/// `start()` attaches to any agent that answers `status` rather than launching a
/// duplicate. After an update that agent keeps running the OLD code (its binary
/// was replaced on disk, not in memory), so fixes never take effect until the
/// Mac restarts. An agent that started before its executable was last written
/// is stale and is stopped so the current build can be launched.
enum StaleAgent {
    /// PIDs of this user's processes running `executable` with `run` as the
    /// first argument, that started before `executable` was last modified.
    static func pids(for executable: URL) -> [pid_t] {
        guard let attrs = try? FileManager.default.attributesOfItem(atPath: executable.path),
              let modified = attrs[.modificationDate] as? Date else { return [] }
        let target = executable.resolvingSymlinksInPath().path
        let uid = getuid()
        return allPIDs().filter { pid in
            guard pid > 0, pid != getpid(), let info = kinfo(pid), info.kp_eproc.e_ucred.cr_uid == uid,
                  path(of: pid) == target, firstArgument(of: pid) == "run" else { return false }
            let tv = info.kp_proc.p_un.__p_starttime
            let started = Date(timeIntervalSince1970: TimeInterval(tv.tv_sec) + TimeInterval(tv.tv_usec) / 1e6)
            return started < modified
        }
    }

    /// SIGTERM each stale agent, then wait up to five seconds for all to exit.
    /// Returns true when none is left.
    @discardableResult
    static func stop(for executable: URL) -> Bool {
        let stale = pids(for: executable)
        guard !stale.isEmpty else { return true }
        stale.forEach { kill($0, SIGTERM) }
        for _ in 0..<50 {
            if stale.allSatisfy({ kill($0, 0) != 0 }) { return true }
            usleep(100_000)
        }
        return false
    }

    private static func allPIDs() -> [pid_t] {
        let count = proc_listallpids(nil, 0)
        guard count > 0 else { return [] }
        var pids = [pid_t](repeating: 0, count: Int(count) * 2)
        let filled = pids.withUnsafeMutableBufferPointer {
            proc_listallpids($0.baseAddress, Int32($0.count * MemoryLayout<pid_t>.size))
        }
        return Array(pids.prefix(max(0, Int(filled))))
    }

    private static func kinfo(_ pid: pid_t) -> kinfo_proc? {
        var info = kinfo_proc()
        var size = MemoryLayout<kinfo_proc>.stride
        var mib: [Int32] = [CTL_KERN, KERN_PROC, KERN_PROC_PID, pid]
        guard sysctl(&mib, u_int(mib.count), &info, &size, nil, 0) == 0, size > 0 else { return nil }
        return info
    }

    private static func path(of pid: pid_t) -> String? {
        var buffer = [CChar](repeating: 0, count: 4 * Int(MAXPATHLEN))
        guard proc_pidpath(pid, &buffer, UInt32(buffer.count)) > 0 else { return nil }
        return URL(fileURLWithPath: String(cString: buffer)).resolvingSymlinksInPath().path
    }

    /// argv[1] via KERN_PROCARGS2: argc, exec path, padding NULs, then argv.
    private static func firstArgument(of pid: pid_t) -> String? {
        var mib: [Int32] = [CTL_KERN, KERN_PROCARGS2, pid]
        var size = 0
        guard sysctl(&mib, 3, nil, &size, nil, 0) == 0, size > MemoryLayout<Int32>.size else { return nil }
        var bytes = [UInt8](repeating: 0, count: size)
        guard sysctl(&mib, 3, &bytes, &size, nil, 0) == 0 else { return nil }
        let argc = bytes.withUnsafeBytes { $0.load(as: Int32.self) }
        guard argc >= 2 else { return nil }
        var i = MemoryLayout<Int32>.size
        while i < size, bytes[i] != 0 { i += 1 }   // exec path
        while i < size, bytes[i] == 0 { i += 1 }   // padding
        while i < size, bytes[i] != 0 { i += 1 }   // argv[0]
        i += 1
        guard i < size else { return nil }
        let start = i
        while i < size, bytes[i] != 0 { i += 1 }
        return String(decoding: bytes[start..<i], as: UTF8.self)
    }
}
