import Foundation
import Virtualization

struct Resources: Equatable {
    var cpus: Int
    var memoryBytes: UInt64
}

/// Pure so it can be unit-tested without the framework.
func clampResources(cpus: Int, memoryMB: Int,
                    minCPU: Int, maxCPU: Int,
                    minMemory: UInt64, maxMemory: UInt64) -> Resources {
    let c = min(max(cpus, minCPU), max(maxCPU, minCPU))
    var m = UInt64(max(memoryMB, 0)) << 20
    m = min(max(m, minMemory), maxMemory)
    m -= m % (1 << 20)
    return Resources(cpus: c, memoryBytes: m)
}

/// Result of building the configuration. `guestBridgeFD` is the host end of the
/// socketpair behind the guest's second console port (nil when guestSocket is empty).
@MainActor
struct BuiltVM {
    let configuration: VZVirtualMachineConfiguration
    let guestBridgeFD: Int32?
}

/// Builds the Virtualization.framework configuration from a validated spec.
/// No VM is started here.
@MainActor
enum VMConfigBuilder {
    static func build(_ spec: VMSpec) throws -> BuiltVM {
        let config = VZVirtualMachineConfiguration()

        let res = clampResources(
            cpus: spec.cpus, memoryMB: spec.memoryMB,
            minCPU: VZVirtualMachineConfiguration.minimumAllowedCPUCount,
            maxCPU: min(VZVirtualMachineConfiguration.maximumAllowedCPUCount, ProcessInfo.processInfo.activeProcessorCount),
            minMemory: VZVirtualMachineConfiguration.minimumAllowedMemorySize,
            maxMemory: VZVirtualMachineConfiguration.maximumAllowedMemorySize)
        config.cpuCount = res.cpus
        config.memorySize = res.memoryBytes

        // Platform with a persisted identity (kept next to the disk).
        let platform = VZGenericPlatformConfiguration()
        platform.machineIdentifier = try loadOrCreateMachineIdentifier(path: spec.diskPath + ".machine-id")
        config.platform = platform

        // UEFI with a persistent variable store next to the disk.
        let efi = VZEFIBootLoader()
        let storeURL = URL(fileURLWithPath: spec.diskPath + ".efivars")
        if FileManager.default.fileExists(atPath: storeURL.path) {
            efi.variableStore = VZEFIVariableStore(url: storeURL)
        } else {
            efi.variableStore = try VZEFIVariableStore(creatingVariableStoreAt: storeURL)
            chmod(storeURL.path, 0o600)
        }
        config.bootLoader = efi

        // Disks: main read-write, optional cloud-init seed read-only.
        var storage: [VZStorageDeviceConfiguration] = []
        let disk = try VZDiskImageStorageDeviceAttachment(url: URL(fileURLWithPath: spec.diskPath), readOnly: false)
        storage.append(VZVirtioBlockDeviceConfiguration(attachment: disk))
        if let seed = spec.seed {
            let att = try VZDiskImageStorageDeviceAttachment(url: URL(fileURLWithPath: seed), readOnly: true)
            storage.append(VZVirtioBlockDeviceConfiguration(attachment: att))
        }
        config.storageDevices = storage

        // NAT network with a stable MAC.
        let net = VZVirtioNetworkDeviceConfiguration()
        net.attachment = VZNATNetworkDeviceAttachment()
        net.macAddress = try resolveMAC(spec: spec)
        var nics = [net]
        // Optional second NIC on the Mac's LAN (a home-network address from the
        // router), bridged by the nexal-vmnet root helper. Best effort: without
        // the helper the VM still boots with NAT, and says why on stderr.
        if let lanSocket = spec.lan {
            do {
                let (attachment, info) = try LANBridge.attach(socketPath: lanSocket, interfaceID: spec.sandboxId)
                let lan = VZVirtioNetworkDeviceConfiguration()
                lan.attachment = attachment
                if let s = spec.lanMacAddress, let mac = VZMACAddress(string: s) { lan.macAddress = mac }
                nics.append(lan)
                FileHandle.standardError.write(Data("nexal-vmhost: LAN bridge up (\(info))\n".utf8))
            } catch {
                FileHandle.standardError.write(Data("nexal-vmhost: \(error); continuing with NAT only\n".utf8))
            }
        }
        config.networkDevices = nics

        config.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
        config.memoryBalloonDevices = [VZVirtioTraditionalMemoryBalloonDeviceConfiguration()]

        // Serial console appended to ConsoleLog (hvc0), guest channel on hvc1.
        var ports: [VZSerialPortConfiguration] = []
        let log = try openAppend(path: spec.consoleLog)
        let console = VZVirtioConsoleDeviceSerialPortConfiguration()
        console.attachment = VZFileHandleSerialPortAttachment(fileHandleForReading: nil, fileHandleForWriting: log)
        ports.append(console)

        var bridgeFD: Int32? = nil
        if spec.guest != nil {
            var fds: [Int32] = [0, 0]
            guard socketpair(AF_UNIX, SOCK_STREAM, 0, &fds) == 0 else {
                throw SpecError("socketpair failed: \(String(cString: strerror(errno)))")
            }
            let vmEnd = FileHandle(fileDescriptor: fds[0], closeOnDealloc: true)
            let guestPort = VZVirtioConsoleDeviceSerialPortConfiguration()
            guestPort.attachment = VZFileHandleSerialPortAttachment(fileHandleForReading: vmEnd, fileHandleForWriting: vmEnd)
            ports.append(guestPort)
            bridgeFD = fds[1]
        }
        config.serialPorts = ports

        // Optional virtio-fs shares.
        var shares: [VZDirectorySharingDeviceConfiguration] = []
        for d in spec.sharedDirs ?? [] {
            try VZVirtioFileSystemDeviceConfiguration.validateTag(d.tag)
            let fs = VZVirtioFileSystemDeviceConfiguration(tag: d.tag)
            fs.share = VZSingleDirectoryShare(directory: VZSharedDirectory(url: URL(fileURLWithPath: d.path), readOnly: d.readOnly ?? false))
            shares.append(fs)
        }
        config.directorySharingDevices = shares

        // Headless unless the spec asks for a GPU.
        if spec.wantsDesktop {
            let gpu = VZVirtioGraphicsDeviceConfiguration()
            gpu.scanouts = [VZVirtioGraphicsScanoutConfiguration(widthInPixels: 1920, heightInPixels: 1080)]
            config.graphicsDevices = [gpu]
            config.keyboards = [VZUSBKeyboardConfiguration()]
            config.pointingDevices = [VZUSBScreenCoordinatePointingDeviceConfiguration()]
        }

        try config.validate()
        return BuiltVM(configuration: config, guestBridgeFD: bridgeFD)
    }

    private static func loadOrCreateMachineIdentifier(path: String) throws -> VZGenericMachineIdentifier {
        let url = URL(fileURLWithPath: path)
        if let data = try? Data(contentsOf: url) {
            guard let id = VZGenericMachineIdentifier(dataRepresentation: data) else {
                throw SpecError("corrupt machine identifier file \(path)")
            }
            return id
        }
        let id = VZGenericMachineIdentifier()
        try id.dataRepresentation.write(to: url, options: .atomic)
        chmod(path, 0o600)
        return id
    }

    /// Spec MAC wins; otherwise a generated locally administered MAC persisted next to the disk.
    private static func resolveMAC(spec: VMSpec) throws -> VZMACAddress {
        if let s = spec.macAddress, !s.isEmpty {
            guard let mac = VZMACAddress(string: s) else { throw SpecError("invalid macAddress") }
            return mac
        }
        let path = spec.diskPath + ".mac"
        if let s = try? String(contentsOfFile: path, encoding: .utf8),
           let mac = VZMACAddress(string: s.trimmingCharacters(in: .whitespacesAndNewlines)) {
            return mac
        }
        let mac = VZMACAddress.randomLocallyAdministered()
        try (mac.string + "\n").write(toFile: path, atomically: true, encoding: .utf8)
        chmod(path, 0o600)
        return mac
    }

    private static func openAppend(path: String) throws -> FileHandle {
        let fd = open(path, O_WRONLY | O_APPEND | O_CREAT | O_NOFOLLOW, 0o600)
        guard fd >= 0 else { throw SpecError("cannot open console log \(path): \(String(cString: strerror(errno)))") }
        return FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    }
}
