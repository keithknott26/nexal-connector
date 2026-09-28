import Foundation
import dnssd

/// Publishes only this Mac's assigned gateway share to its local Bonjour client.
/// Nothing is multicast onto the LAN, and backup bytes still go directly to OVH.
@MainActor
final class TimeMachineDiscovery {
    private var references: [DNSServiceRef] = []
    private var current: String?

    static func eligible(_ state: TimeMachineReport.State?, connected: Bool) -> Bool {
        guard connected, let state, state.role == "client", state.enabled,
              state.entitled, state.serviceState == "ready",
              let host = state.host, (host.hasSuffix(".mesh.nexal.systems") || privateIPv4(host) != nil),
              let share = state.shareName,
              share.range(of: "^tm[a-f0-9]{20}$", options: .regularExpression) != nil else { return false }
        return true
    }

    static func privateIPv4(_ host: String) -> [UInt8]? {
        let parts = host.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 4 else { return nil }
        let bytes = parts.compactMap { UInt8($0) }
        guard bytes.count == 4, bytes[0] == 100, (64...127).contains(bytes[1]) else { return nil }
        return bytes
    }

    func stop() {
        references.forEach { DNSServiceRefDeallocate($0) }
        references.removeAll()
        current = nil
    }

    func update(_ state: TimeMachineReport.State?, connected: Bool) {
        guard Self.eligible(state, connected: connected), let state,
              let host = state.host, let share = state.shareName else { stop(); return }
        let identity = host + "/" + share
        guard current != identity else { return }
        stop()
        let name = "neXal Time Machine"
        var target = host + "."
        if let address = Self.privateIPv4(host) {
            target = "nexal-\(share).local."
            var connection: DNSServiceRef?
            guard DNSServiceCreateConnection(&connection) == kDNSServiceErr_NoError,
                  let connection else { return }
            references.append(connection)
            var record: DNSRecordRef?
            let error = address.withUnsafeBytes { bytes in
                DNSServiceRegisterRecord(connection, &record, DNSServiceFlags(kDNSServiceFlagsUnique),
                    UInt32(kDNSServiceInterfaceIndexLocalOnly), target, UInt16(kDNSServiceType_A),
                    UInt16(kDNSServiceClass_IN), 4, bytes.baseAddress, 60, { _, _, _, _, _ in }, nil)
            }
            guard error == kDNSServiceErr_NoError else { stop(); return }
            guard DNSServiceSetDispatchQueue(connection, .main) == kDNSServiceErr_NoError else { stop(); return }
        }
        let records: [(String, UInt16, [String: Data])] = [
            ("_smb._tcp", 445, [:]),
            ("_adisk._tcp", 9, ["sys": Data("waMA=0,adVF=0x100".utf8),
                                "dk0": Data("adVN=\(share),adVF=0x82".utf8)])
        ]
        for (type, port, values) in records {
            let txt = NetService.data(fromTXTRecord: values)
            var ref: DNSServiceRef?
            let error = txt.withUnsafeBytes { bytes in
                DNSServiceRegister(&ref, 0, UInt32(kDNSServiceInterfaceIndexLocalOnly),
                                   name, type, "local.", target, port.bigEndian,
                                   UInt16(txt.count), bytes.baseAddress, nil, nil)
            }
            guard error == kDNSServiceErr_NoError, let ref else { stop(); return }
            references.append(ref)
        }
        current = identity
    }
}
