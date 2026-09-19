// Experimental diagnostic, not a remote-memory implementation.
// Does not launch a VM, change configuration, or allocate large test buffers.
// Native compilation/execution remains to be checked on macOS.
import Foundation

#if os(macOS)
import Virtualization
import Metal

if #available(macOS 11.0, *) {
    let minimum = VZVirtualMachineConfiguration.minimumAllowedMemorySize
    let maximum = VZVirtualMachineConfiguration.maximumAllowedMemorySize
    let requestedGB: UInt64 = 100_000_000_000
    let requestedGiB: UInt64 = 100 * 1_024 * 1_024 * 1_024

    var output: [String: Any] = [
        "schemaVersion": 1,
        "purpose": "Capability inspection only; no VM or remote memory test",
        "physicalMemoryBytes": ProcessInfo.processInfo.physicalMemory,
        "vmMinimumAllowedMemoryBytes": minimum,
        "vmMaximumAllowedMemoryBytes": maximum,
        "proposal100GBBytes": requestedGB,
        "proposal100GiBBytes": requestedGiB,
        "proposal100GBWithinAPIRange": requestedGB >= minimum && requestedGB <= maximum,
        "proposal100GiBWithinAPIRange": requestedGiB >= minimum && requestedGiB <= maximum,
        "completeVMConfigurationValidated": false,
        "remoteMemoryTested": false,
        "largeMetalAllocationTested": false,
        "warning": "API limits are not free capacity, guaranteed allocation, remote backing, or GPU residency."
    ]

    if let device = MTLCreateSystemDefaultDevice() {
        output["metal"] = [
            "available": true,
            "deviceName": device.name,
            "hasUnifiedMemory": device.hasUnifiedMemory,
            "recommendedMaxWorkingSetSizeBytes": device.recommendedMaxWorkingSetSize,
            "maxBufferLengthBytes": device.maxBufferLength
        ] as [String: Any]
    } else {
        output["metal"] = ["available": false]
    }

    do {
        let data = try JSONSerialization.data(
            withJSONObject: output,
            options: [.prettyPrinted, .sortedKeys]
        )
        FileHandle.standardOutput.write(data)
        FileHandle.standardOutput.write(Data("\n".utf8))
    } catch {
        FileHandle.standardError.write(Data("Could not encode capability report.\n".utf8))
        exit(1)
    }
} else {
    FileHandle.standardError.write(Data("This diagnostic requires macOS 11 or later.\n".utf8))
    exit(1)
}
#else
FileHandle.standardError.write(Data("Run this diagnostic on the target Mac, not Linux.\n".utf8))
exit(1)
#endif
