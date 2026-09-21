// Package discovery locates candidate Nexal Macs. It never authorizes one.
//
// Three separable problems, three mechanisms:
//
//	LAN discovery         mDNS/DNS-SD, _nexal._tcp.local.   mdns.go
//	WAN discovery         coordinator rendezvous            rendezvous.go
//	WAN connection (NAT)  ICE — deferred, not in this pass   see peers.go
//
// There is no DHT and no gossip overlay: WAN "discovery" is an authenticated list
// read from the control plane this host already holds a token for.
//
// The invariant, which every file here is shaped by: discovery produces
// candidates, and the sole authority on whether a peer may read or write a Store
// remains the mutual-TLS peerPolicy in internal/pool, which pins an exact ed25519
// device fingerprint. An mDNS TXT record is an unauthenticated claim from whoever
// happens to be on the local network — a café, a hotel floor, a co-working
// space — so it may select which fingerprint to attempt and may never add one to
// AllowedPeers. Only the coordinator's list may source AllowedPeers. A peer seen
// on mDNS but absent from that list is displayable as "found, not authorized" and
// is never dialed with credentials.
//
// Two consequences worth stating because they are easy to lose while optimizing:
// locality is a transport hint that grants nothing, and sharing is gated off by
// default (SharingPolicy) because whether it should default on is an unresolved
// product decision and not one this package makes.
//
// Nothing here relays bytes. No code path in this package sends pager or file
// data through a Cloudflare service.
package discovery
