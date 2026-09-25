import Foundation

/// Public IP and "Town, ST, Country" for a peer, shown beside its name.
struct PeerNetInfo: Equatable {
    var publicAddress: String?
    var location: String?
}

/// IP geolocation through ipapi.co (HTTPS, no key). Only public addresses are
/// looked up; results are cached (24h per IP, 30 min for this Mac's own public
/// IP) to stay far below the free tier's daily limit.
actor PeerLocator {
    static let shared = PeerLocator()

    private struct Entry { let at: Date; let location: String? }
    private var cache: [String: Entry] = [:]
    private var own: (at: Date, ip: String?, location: String?)?
    private let session: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 6
        return URLSession(configuration: config)
    }()

    private struct Reply: Decodable {
        let ip: String?
        let city: String?
        let region_code: String?
        let country_name: String?
        let error: Bool?
    }

    /// This Mac's public IP (what peers on the same LAN share too).
    func ownPublicIP() async -> (ip: String?, location: String?) {
        if let own, Date().timeIntervalSince(own.at) < 1800 { return (own.ip, own.location) }
        let reply = await fetch("https://ipapi.co/json/")
        let result = (reply?.ip, reply.flatMap(Self.format))
        own = (Date(), result.0, result.1)
        if let ip = result.0 { cache[ip] = Entry(at: Date(), location: result.1) }
        return result
    }

    func location(for ip: String) async -> String? {
        if let hit = cache[ip], Date().timeIntervalSince(hit.at) < 86_400 { return hit.location }
        guard Self.isPublic(ip), let encoded = ip.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) else { return nil }
        let reply = await fetch("https://ipapi.co/\(encoded)/json/")
        let location = reply.flatMap(Self.format)
        // Cache misses too, so a failing lookup is not retried on every poll.
        cache[ip] = Entry(at: Date(), location: location)
        return location
    }

    /// Public IP and location for a peer: its direct endpoint when that is
    /// public, otherwise (same LAN) this Mac's own public IP.
    func info(directAddress: String?, directIsPrivate: Bool) async -> PeerNetInfo {
        if let direct = directAddress, !directIsPrivate, Self.isPublic(direct) {
            return PeerNetInfo(publicAddress: direct, location: await location(for: direct))
        }
        guard directAddress != nil else { return PeerNetInfo() }
        let mine = await ownPublicIP()
        return PeerNetInfo(publicAddress: mine.ip, location: mine.location)
    }

    private func fetch(_ string: String) async -> Reply? {
        guard let url = URL(string: string) else { return nil }
        var request = URLRequest(url: url)
        request.setValue("neXal-Connector", forHTTPHeaderField: "User-Agent")
        guard let (data, response) = try? await session.data(for: request),
              (response as? HTTPURLResponse)?.statusCode == 200,
              let reply = try? JSONDecoder().decode(Reply.self, from: data),
              reply.error != true else { return nil }
        return reply
    }

    private static func format(_ r: Reply) -> String? {
        let parts = [r.city, r.region_code, r.country_name]
            .compactMap { $0?.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        return parts.isEmpty ? nil : parts.joined(separator: ", ")
    }

    static func isPublic(_ ip: String) -> Bool {
        if ip.contains(":") {
            let lower = ip.lowercased()
            return !(lower.hasPrefix("fe80") || lower.hasPrefix("fc") || lower.hasPrefix("fd") || lower == "::1")
        }
        let o = ip.split(separator: ".").compactMap { Int($0) }
        guard o.count == 4 else { return false }
        switch (o[0], o[1]) {
        case (10, _), (127, _), (169, 254), (192, 168), (0, _): return false
        case (172, 16...31), (100, 64...127): return false
        default: return true
        }
    }
}
