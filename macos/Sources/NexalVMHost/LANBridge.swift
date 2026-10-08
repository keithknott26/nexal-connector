import Foundation
import Virtualization
import NexalVMNetClient

/// A NIC on the Mac's LAN through the nexal-vmnet root helper (macos/vmnet): the
/// helper owns a vmnet bridged interface and relays Ethernet frames over a
/// datagram socket, which Virtualization.framework attaches with
/// VZFileHandleNetworkDeviceAttachment -- how Lima's vz driver uses socket_vmnet.
/// (Bridging natively needs Apple's restricted com.apple.vm.networking entitlement.)
@MainActor
enum LANBridge {
    /// The helper keeps the bridge up while this connection is open, i.e. for the
    /// life of this process; it is never closed explicitly.
    private static var controlFD: Int32 = -1

    /// Asks the helper for a bridge. Returns the attachment and "<interface> <mtu>".
    static func attach(socketPath: String, interfaceID: String) throws -> (VZFileHandleNetworkDeviceAttachment, String) {
        var vmFD: Int32 = -1
        var info = [CChar](repeating: 0, count: 256)
        let control = nexal_vmnet_attach(socketPath, interfaceID, &vmFD, &info, info.count)
        let text = String(cString: info)
        guard control >= 0, vmFD >= 0 else { throw SpecError("LAN bridge unavailable: \(text)") }
        controlFD = control
        let handle = FileHandle(fileDescriptor: vmFD, closeOnDealloc: true)
        return (VZFileHandleNetworkDeviceAttachment(fileHandle: handle), text)
    }
}
