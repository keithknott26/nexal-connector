package discovery

import (
	"net/netip"
	"time"
)

// Statically configured peers, merged into the same candidate view as mDNS and
// the coordinator directory — and subject to exactly the same rule as both:
//
//	CONFIGURATION NEVER AUTHORIZES (HARDENING-PLAN §30.2).
//
// A Static entry is the owner saying "this fingerprint lives at this address".
// That is an ADDRESS, contributed for dialling, plus the fingerprint used to
// look the peer up. It never sets Peer.Authorized, so AllowedPeers — which reads
// Authorized and nothing else — cannot grow because somebody edited a config
// file. The difference from mDNS is provenance and trust in the CLAIM (the owner
// typed it, rather than an anonymous multicast packet asserting it), not
// permission: the mutual-TLS handshake in internal/pool still decides who may
// read or write a Store.
//
// Why this exists at all: mDNS is link-local (224.0.0.251 / ff02::fb) and a
// router does not forward it, so two Macs on separate VLANs never see each
// other's announcements — even though the peer transport would happily dial
// across the router, because an RFC1918 address on another subnet already
// satisfies pool.privateIP. Static entries supply the address that discovery
// cannot. See TRANSPORT-NAT-DESIGN.md.

// Static is one owner-configured dial candidate. The endpoint string shape and
// its private-IP validation live in internal/config (which calls
// pool.ValidPeerEndpoint); by the time an entry reaches this package it is an
// address and a port.
type Static struct {
	Fingerprint string
	Address     netip.AddrPort
	Label       string
}

// MergeStatic is Merge plus owner-configured endpoints.
//
// Merge itself is unchanged and still exists: a caller with no static peers gets
// exactly the previous behaviour, and the asymmetry between the three sources
// stays visible in one function rather than being spread across three.
//
//	rendezvous  identity + AUTHORIZATION + addresses
//	mDNS        addresses + same-link evidence, authorization never
//	static      addresses only, authorization never, marked Configured
func MergeStatic(authorized Snapshot, seen []Candidate, static []Static, link LinkView, now time.Time) []Peer {
	peers := Merge(authorized, seen, link, now)
	byFingerprint := map[string]int{}
	for i := range peers {
		byFingerprint[peers[i].Fingerprint] = i
	}
	for _, s := range static {
		if !ValidFingerprint(s.Fingerprint) || !s.Address.IsValid() || s.Address.Port() == 0 {
			continue
		}
		index, known := byFingerprint[s.Fingerprint]
		if !known {
			// Configured but not in the coordinator's set and not heard on the
			// link: a display row that says "configured, not authorized". It is
			// deliberately visible rather than hidden, because an owner who typed
			// an address wants to know the reason nothing happens is
			// authorization, not a typo.
			peers = append(peers, Peer{
				HostID: "", Name: s.Label, Fingerprint: s.Fingerprint,
				Source: SourceStatic, Authorized: false, Configured: true,
				Locality:  link.Classify(s.Address.Addr(), false),
				Addresses: []netip.AddrPort{s.Address},
			})
			byFingerprint[s.Fingerprint] = len(peers) - 1
			continue
		}
		p := &peers[index]
		p.Configured = true
		if p.Source == 0 {
			p.Source = SourceStatic
		}
		// The configured address goes FIRST: the owner named it because discovery
		// cannot reach this peer, so it is the candidate most likely to work. It
		// is still only a candidate — the TLS handshake decides whether whoever
		// answers is the right machine.
		p.Addresses = prependAddress(p.Addresses, s.Address)
		p.Locality = closer(p.Locality, link.Classify(s.Address.Addr(), false))
	}
	return peers
}

func prependAddress(list []netip.AddrPort, add netip.AddrPort) []netip.AddrPort {
	for i, existing := range list {
		if existing == add {
			// Already present, possibly from the directory. Move it to the front
			// rather than duplicating it.
			out := append([]netip.AddrPort{add}, list[:i]...)
			return append(out, list[i+1:]...)
		}
	}
	if len(list) >= 16 {
		// Bounded exactly like appendAddress: drop the tail, keep the configured
		// address, because the owner's entry is the one with a stated reason.
		list = list[:15]
	}
	return append([]netip.AddrPort{add}, list...)
}
