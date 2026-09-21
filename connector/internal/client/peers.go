package client

import (
	"context"
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

// The coordinator is the rendezvous server for WAN discovery. There is no
// broadcast on the internet, so "WAN discovery" is an authenticated list read
// from a control plane this host already holds a token for — no DHT, no gossip.
//
// What comes back is a candidate list. It is authorization only in the sense that
// the coordinator decided these peers belong to the same tenant and are approved,
// unrevoked and unpaused; the actual admission decision still happens per request
// in the mutual-TLS peer policy.

// PeerAddress is one dial candidate for a peer. Kind is "lan" or "wan". A wan
// address is server-observed: a host does not get to tell the coordinator its own
// public address, so a wan kind here was never asserted by the peer.
type PeerAddress struct {
	Kind    string `json:"kind"`
	Address string `json:"address"`
	Port    uint16 `json:"port"`
	// ObservedAt is the coordinator's ISO-8601 timestamp. Declared because the
	// client decodes strictly; see the forward-compatibility note on PeerDirectory.
	ObservedAt string `json:"observedAt"`
}

// PeerCapabilities is self-reported by the peer and unattested by anything. The
// field naming says so at every layer on purpose: these values may not alone
// unlock a gated path, and pool.PlanMLX already treats them as necessary rather
// than sufficient evidence.
type PeerCapabilities struct {
	ThunderboltGeneration int    `json:"thunderboltGeneration,omitempty"`
	RDMAEnabled           bool   `json:"rdmaEnabled,omitempty"`
	OSVersion             string `json:"osVersion,omitempty"`
	Chip                  string `json:"chip,omitempty"`
}

// DirectoryPeer is one same-tenant peer. A host without a device fingerprint is
// omitted by the coordinator, because a peer that cannot be cryptographically
// authenticated could only be dialed unauthenticated.
type DirectoryPeer struct {
	HostID                   string           `json:"hostId"`
	Name                     string           `json:"name"`
	Fingerprint              string           `json:"fingerprint"`
	Addresses                []PeerAddress    `json:"addresses"`
	SelfReportedCapabilities PeerCapabilities `json:"capabilities"`
	// LastSeenAt and CapabilitiesReportedAt are coordinator-assigned ISO-8601
	// timestamps. CapabilitiesReportedAt is null when a peer has never reported;
	// encoding/json leaves the zero value for null, which is the intended reading.
	LastSeenAt             string `json:"lastSeenAt"`
	CapabilitiesReportedAt string `json:"capabilitiesReportedAt"`
}

// PeerDirectory carries the coordinator's two honesty fields verbatim. They are
// checked, not decoration: a response that does not say
// "candidate-list-not-authorization" is a response from something that does not
// share this contract, and it is refused rather than interpreted.
// Every field the coordinator sends must be declared here, because c.call decodes
// with DisallowUnknownFields. That strictness is deliberate for config files and
// untrusted bundles, but it makes this struct a hard coupling to the coordinator's
// response: the Worker deploys instantly while installed connectors lag, so a new
// server field breaks every old client at once. See HARDENING-PLAN §43.
type PeerDirectory struct {
	Peers              []DirectoryPeer `json:"peers"`
	Authorization      string          `json:"authorization"`
	CapabilityEvidence string          `json:"capabilityEvidence"`
	// IdentityEvidence states that a fingerprint is enrollment-bound, not hardware
	// attested. Checked, not decorative, for the same reason as Authorization.
	IdentityEvidence string `json:"identityEvidence"`
	// PeerLimit and Truncated say whether the fleet exceeded the server page. A
	// truncated directory is not a complete view of the tenant and must never be
	// treated as one.
	PeerLimit int  `json:"peerLimit"`
	Truncated bool `json:"truncated"`
	// FreshnessSeconds is the lease window, published so a connector does not have
	// to guess how stale an omitted peer had to be.
	FreshnessSeconds int `json:"freshnessSeconds"`
}

// AuthorizationCandidateList and CapabilityEvidenceSelfReported are the exact
// strings the API contract specifies.
const (
	AuthorizationCandidateList     = "candidate-list-not-authorization"
	CapabilityEvidenceSelfReported = "self-reported-not-attested"
	// IdentityEvidenceSelfReported is the exact string apps/coordinator/src/peers.ts
	// sends as IDENTITY_EVIDENCE. It is compared literally and nothing else is
	// accepted.
	//
	// An earlier revision of this file demanded "enrollment-bound-not-hardware-attested",
	// which appears nowhere in the platform repo: it was invented, and it refused
	// every real directory read. Accepting both spellings was then proposed as the
	// fix, and that is wrong for the reason this check exists. The comparison is how
	// the client refuses a response from something that does not share the contract,
	// so widening it to admit a string the coordinator never sends weakens the gate
	// while hiding the original mistake. A rename is a coordinated two-repo change,
	// and TestHonestyStringsAndCapsMatchCoordinatorSource fails if peers.ts drifts.
	IdentityEvidenceSelfReported = "self-reported-not-proof-of-key-possession"
	// maxDirectoryPeers must not be below the coordinator's MAX_PEERS
	// (apps/coordinator/src/peers.ts). The client rejects the entire directory
	// when the count is exceeded, so a cap lower than the server's page size
	// silently disables discovery for any tenant large enough to fill a page.
	maxDirectoryPeers = 200
	// MaxAdvertisedLANAddresses and MaxObservedWANAddresses mirror MAX_LAN_ADDRESSES
	// and MAX_WAN_ADDRESSES in apps/coordinator/src/peers.ts, which are in turn the
	// `host_address_cap` trigger in migration 0013. Advertising above the cap is
	// refused here rather than sent, because the server refuses the whole request:
	// a partially applied advertise does not exist, so an over-cap batch would
	// leave the host's previous address set in place while the caller believed it
	// had published a new one.
	MaxAdvertisedLANAddresses = 8
	// MaxObservedWANAddresses is 1 because the WAN address is the coordinator's
	// single observation of the connecting address, not a list a host supplies.
	MaxObservedWANAddresses            = 1
	maxDirectoryAddressesPerPeer       = MaxAdvertisedLANAddresses + MaxObservedWANAddresses
	errDirectorySchema                 = "invalid coordinator peer directory schema"
	errDirectoryUnauthorizedAssumption = "coordinator peer directory did not declare itself a candidate list"
	errAdvertiseSchema                 = "invalid coordinator advertise response schema"
	errAdvertiseUnauthorizedAssumption = "coordinator advertise response did not declare itself a candidate list"
)

// acceptedIdentityEvidence is the honesty check for identityEvidence: an exact
// match against the one literal contract string, and nothing else.
func acceptedIdentityEvidence(s string) bool {
	return s == IdentityEvidenceSelfReported
}

// Peers reads the tenant's peer candidates. The tenant is never sent: it is read
// server-side from the host token's row.
func (c *Client) Peers(ctx context.Context) (PeerDirectory, error) {
	var out PeerDirectory
	if err := c.call(ctx, "GET", "/api/peers", nil, &out); err != nil {
		return PeerDirectory{}, err
	}
	if out.Authorization != AuthorizationCandidateList {
		return PeerDirectory{}, errors.New(errDirectoryUnauthorizedAssumption)
	}
	if out.CapabilityEvidence != CapabilityEvidenceSelfReported ||
		!acceptedIdentityEvidence(out.IdentityEvidence) ||
		len(out.Peers) > maxDirectoryPeers {
		return PeerDirectory{}, errors.New(errDirectorySchema)
	}
	for _, p := range out.Peers {
		if !ValidID(p.HostID) || len(p.Name) > 256 || len(p.Addresses) > maxDirectoryAddressesPerPeer {
			return PeerDirectory{}, errors.New(errDirectorySchema)
		}
		for _, a := range p.Addresses {
			if a.Kind != "lan" && a.Kind != "wan" {
				return PeerDirectory{}, errors.New(errDirectorySchema)
			}
		}
	}
	return out, nil
}

// LANAddress is one address this host publishes as a dial candidate. Kind is
// always "lan": apps/coordinator/src/peers.ts refuses kind "wan" with a 400
// rather than ignoring it, because the public address is the coordinator's
// observation to make and accepting a host's claim about it would let a host
// point its peers at an arbitrary third party (§3.2).
//
// All three fields are always emitted. The server validates the object with
// `keys(item, ["kind","address","port"])`, whose `required` defaults to the
// allowed list, so an omitted port is a 400 rather than a default.
type LANAddress struct {
	Kind    string `json:"kind"`
	Address string `json:"address"`
	Port    uint16 `json:"port"`
}

// Advertisement is the POST /api/peers/advertise request body. The server
// accepts exactly {fingerprint, addresses, capabilities} and requires the first
// two, so Capabilities is a pointer: omitting it is meaningful, and means
// "clear the stored self-report" rather than "leave it alone" — advertise is a
// full replacement of this host's published set, not a patch.
type Advertisement struct {
	// Fingerprint is pool.DeviceID: 64 lowercase hex characters of SHA-256 over
	// the ed25519 public key, the same value peer TLS pins. Uppercase is a 400
	// on the server and is refused here, so both sides publish identical bytes.
	Fingerprint string `json:"fingerprint"`
	// Addresses must serialize as a JSON array even when empty: the server tests
	// Array.isArray, and a nil Go slice encodes as null, which is a 400. Advertise
	// therefore builds this slice itself rather than forwarding a caller's nil.
	Addresses    []LANAddress      `json:"addresses"`
	Capabilities *PeerCapabilities `json:"capabilities,omitempty"`
}

// AdvertiseAck is the literal response body of POST /api/peers/advertise.
//
// EVERY field the coordinator returns must be declared, because c.call decodes
// with DisallowUnknownFields: an undeclared field is not ignored, it fails the
// whole call at runtime. HARDENING-PLAN §43 records this exact bug being shipped
// twice, so the field list below is copied from the return statement of
// advertisePeer in apps/coordinator/src/peers.ts, not from what this client
// happens to need.
type AdvertiseAck struct {
	OK          bool   `json:"ok"`
	Fingerprint string `json:"fingerprint"`
	// LANAddresses is the number of lan candidates the coordinator stored, after
	// its own duplicate collapsing. It is the count, not the list.
	LANAddresses int `json:"lanAddresses"`
	// ObservedWANAddress is the coordinator's own observation of the connecting
	// address, and the only source of a wan candidate. It is JSON null when the
	// request did not arrive through a Cloudflare edge (local development), which
	// encoding/json leaves as the empty string: no WAN candidate was recorded,
	// which is the honest reading rather than a fabricated public address.
	ObservedWANAddress string `json:"observedWanAddress"`
	IdentityEvidence   string `json:"identityEvidence"`
	CapabilityEvidence string `json:"capabilityEvidence"`
	Authorization      string `json:"authorization"`
}

// lanAddressClaim mirrors `lanAddress` in apps/coordinator/src/peers.ts so a
// request the server would reject is not sent. It returns the normalized form.
//
// The accepted families are exactly the server's three: RFC1918, 100.64/10
// CGNAT, and link-local (169.254/16, fe80::/10). IPv6 unique-local (fc00::/7) is
// deliberately NOT accepted even though netip.Addr.IsPrivate reports it as
// private — widening the contract to a fourth family is a policy decision, not a
// client-side convenience. Loopback is not accepted either: 127.0.0.1 is in none
// of the three families, so publishing it would be a guaranteed 400.
//
// A zone index (fe80::1%en0) is rejected rather than stripped: the zone is
// meaningful only on this machine, and the server stores no interface names.
func lanAddressClaim(s string) (string, bool) {
	if len(s) < 3 || len(s) > 45 || s != strings.ToLower(s) || strings.Contains(s, "%") {
		return "", false
	}
	addr, err := netip.ParseAddr(s)
	if err != nil || addr.Zone() != "" {
		return "", false
	}
	if addr.Is4() {
		// Re-rendered from the parsed value, which also rejects the leading-zero
		// forms the server refuses: "010.0.0.1" is octal to one resolver and
		// decimal to another, so the same string would name two hosts.
		if !addr.IsPrivate() && !cgnatAddress(addr) && !addr.IsLinkLocalUnicast() {
			return "", false
		}
		return addr.String(), true
	}
	if addr.Is4In6() || !addr.IsLinkLocalUnicast() {
		return "", false
	}
	return addr.String(), true
}

// cgnatAddress covers 100.64.0.0/10, which netip does not consider private but
// the coordinator accepts as a lan family.
func cgnatAddress(addr netip.Addr) bool {
	return addr.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(addr)
}

// ValidLANAddressClaim reports whether an address may be published as a lan
// candidate, and returns the exact normalized string the coordinator will store.
// Callers gathering interface addresses use it to filter before advertising,
// instead of learning about a rejected family from an HTTP 400.
func ValidLANAddressClaim(s string) (string, bool) { return lanAddressClaim(s) }

// Advertise publishes this host's fingerprint and lan dial candidates, and
// returns what the coordinator recorded.
//
// This is what makes the peer directory non-empty: without it the coordinator's
// host_addresses table stays empty for this host, and every peer reading
// /api/peers sees this host with no addresses at all.
//
// Publishing is not authorization and Advertise grants nothing. It is a claim
// over an authenticated channel; whether this host may read or write a peer's
// Store is still decided by the mutual-TLS peerPolicy in internal/pool.
func (c *Client) Advertise(ctx context.Context, fingerprint string, addresses []LANAddress, capabilities *PeerCapabilities) (AdvertiseAck, error) {
	if !validFingerprint(fingerprint) {
		return AdvertiseAck{}, errors.New("invalid device fingerprint")
	}
	if len(addresses) > MaxAdvertisedLANAddresses {
		return AdvertiseAck{}, errors.New("too many lan addresses to advertise")
	}
	// Built here, never reused from the caller: the slice must be non-nil so it
	// encodes as [] rather than null, and every entry must be one the server
	// accepts, because one bad entry fails the whole replacement.
	body := Advertisement{Fingerprint: fingerprint,
		Addresses: make([]LANAddress, 0, len(addresses)), Capabilities: capabilities}
	seen := map[string]bool{}
	for _, a := range addresses {
		if a.Kind != "lan" {
			// Including "wan": the server returns wan_not_advertisable, and
			// pretending otherwise here would hide the same contract.
			return AdvertiseAck{}, errors.New("only lan addresses may be advertised")
		}
		if a.Port == 0 {
			return AdvertiseAck{}, errors.New("invalid advertised port")
		}
		address, ok := lanAddressClaim(a.Address)
		if !ok {
			return AdvertiseAck{}, errors.New("advertised address must be RFC1918, CGNAT or link-local")
		}
		key := address + "/" + strconv.Itoa(int(a.Port))
		// Duplicates are collapsed, matching the server: a machine legitimately
		// reports the same address on two interfaces, and the cap counts
		// candidates rather than noise.
		if seen[key] {
			continue
		}
		seen[key] = true
		body.Addresses = append(body.Addresses, LANAddress{Kind: "lan", Address: address, Port: a.Port})
	}
	var out AdvertiseAck
	if err := c.call(ctx, "POST", "/api/peers/advertise", body, &out); err != nil {
		return AdvertiseAck{}, err
	}
	if out.Authorization != AuthorizationCandidateList {
		return AdvertiseAck{}, errors.New(errAdvertiseUnauthorizedAssumption)
	}
	if out.CapabilityEvidence != CapabilityEvidenceSelfReported ||
		!acceptedIdentityEvidence(out.IdentityEvidence) {
		return AdvertiseAck{}, errors.New(errAdvertiseSchema)
	}
	// The echoed fingerprint is the one thing that says the coordinator wrote
	// this host's row and not some other identity's.
	if !out.OK || out.Fingerprint != fingerprint {
		return AdvertiseAck{}, errors.New(errAdvertiseSchema)
	}
	if out.LANAddresses < 0 || out.LANAddresses > len(body.Addresses) {
		// More stored candidates than were sent is not a bigger fleet, it is a
		// response that does not share this contract.
		return AdvertiseAck{}, errors.New(errAdvertiseSchema)
	}
	if out.ObservedWANAddress != "" {
		addr, err := netip.ParseAddr(out.ObservedWANAddress)
		if err != nil || addr.Zone() != "" {
			return AdvertiseAck{}, errors.New(errAdvertiseSchema)
		}
	}
	return out, nil
}

// validFingerprint is pool.DeviceID's exact output shape, duplicated here rather
// than imported so internal/client keeps no dependency on internal/pool. It is
// the same rule as discovery.ValidFingerprint and the server's
// device_fingerprint_shape constraint: 64 lowercase hex characters.
func validFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
