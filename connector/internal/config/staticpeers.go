package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Static cross-VLAN peers: the owner names a peer's address because mDNS cannot
// reach it.
//
// WHY THIS EXISTS, and it is a smaller change than it looks. The peer transport
// already permits a peer on a different subnet: pool.privateIP accepts any
// RFC1918 address, and 10.20.0.5 satisfies ip.IsPrivate() exactly as 192.168.1.5
// does. What does NOT cross a VLAN is DISCOVERY — mDNS is link-local multicast
// (224.0.0.251 / ff02::fb) and routers do not forward it. So two Macs on
// separate VLANs need a way to SUPPLY an address, not a relaxation of the
// security rule. This file is that way, and it relaxes nothing:
// StaticPeer.Validate applies the SAME rule pool.ValidPeerEndpoint applies.
// See TRANSPORT-NAT-DESIGN.md.
//
// WHY THE RULE IS RE-STATED HERE RATHER THAN IMPORTED. internal/pool cannot be
// imported from this package: pool's own tests import internal/discovery, which
// imports internal/client, which imports this package, so the import would be a
// cycle. internal/discovery already keeps a deliberate second copy
// (localAddress, "kept separate on purpose: this package must not be able to
// loosen that function by editing it") and this follows that precedent — with
// the drift risk closed by a test rather than by a comment:
// TestStaticPeerEndpointRuleMatchesPool imports pool from the TEST binary (legal,
// since pool does not import config) and asserts the two functions agree
// accept-for-accept and reject-for-reject on a table of endpoints. If anyone
// loosens either side, that test fails.
//
// AND IT AUTHORIZES NOTHING (HARDENING-PLAN §30.2: discovery/configuration never
// establishes membership or authorization). A static entry contributes an address
// and the fingerprint to PIN, nothing more. pool.PeerOptions.AllowedPeers is
// still derived from the coordinator's authorized set alone, which is why
// ExpectedFingerprint is REQUIRED here: peer.go:28 states that "AllowedPeers is
// an exact device-fingerprint allowlist, not 'trust the LAN'", and an owner
// typing an IP address must not become a weaker version of that. A configured
// peer the coordinator has not authorized shows up as "configured, not
// authorized" and is never dialled with credentials.

// MaxStaticPeers bounds the list. It is generous for the intended use (a handful
// of the owner's own machines across VLANs) and bounded because an unbounded
// config list is an unbounded dial fan-out.
const MaxStaticPeers = 32

// StaticPeer is one owner-configured peer endpoint.
type StaticPeer struct {
	// Endpoint is an HTTPS URL with a literal private IP and an explicit port,
	// exactly the shape pool.NewPeerClient accepts. A hostname is refused: the
	// peer transport sets Proxy:nil and performs no DNS lookup, so a name here
	// would be a setting that silently never works.
	Endpoint string `json:"endpoint"`
	// Fingerprint is the expected pool.DeviceID (64 lowercase hex). It is the
	// value peer TLS pins, and it is mandatory: an address without a fingerprint
	// would be an invitation to talk to whoever holds that address now.
	Fingerprint string `json:"fingerprint"`
	// Label is an optional owner-readable note ("studio VLAN 20"). It is
	// display-only and is never matched against anything.
	Label string `json:"label,omitempty"`
}

var errStaticPeer = errors.New("a static peer needs an https endpoint with a literal private IP and explicit port, plus the peer's 64-hex device fingerprint")

// Validate checks one entry through the production endpoint rule.
func (p StaticPeer) Validate() error {
	if !ValidPeerEndpoint(p.Endpoint) {
		return errStaticPeer
	}
	if !validDeviceFingerprint(p.Fingerprint) {
		return errors.New("static peer fingerprint must be 64 lowercase hex characters (pool.DeviceID)")
	}
	if len(p.Label) > 64 || strings.ContainsAny(p.Label, "\r\n\t") {
		// A label is printed in CLI JSON and a UI; control characters in it are
		// how a log line or a terminal gets forged.
		return errors.New("static peer label must be at most 64 bytes and contain no control characters")
	}
	return nil
}

// ValidPeerEndpoint mirrors pool.ValidPeerEndpoint exactly: an https URL, no
// credentials, no query, no fragment, no path beyond "/", an explicit numeric
// port, and a host that is a literal PRIVATE, loopback or link-local IP.
//
// Cross-VLAN needs NO relaxation of this: an RFC1918 address on another subnet
// already passes ip.IsPrivate(). A public address is still refused, and a
// hostname is still refused because the peer transport performs no DNS lookup
// (Proxy:nil, explicit endpoint only) and would never resolve it.
func ValidPeerEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Port() == "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() ||
		!(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port >= 1 && port <= 65535
}

// AddrPort is the dial candidate for the merged discovery view. It parses the
// already-validated endpoint, so an invalid entry yields an invalid AddrPort
// rather than a panic.
func (p StaticPeer) AddrPort() (netip.AddrPort, bool) {
	u, err := url.Parse(p.Endpoint)
	if err != nil {
		return netip.AddrPort{}, false
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return netip.AddrPort{}, false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.AddrPort{}, false
	}
	p16, err := net.LookupPort("tcp", port)
	if err != nil || p16 < 1 || p16 > 65535 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(p16)), true
}

// ValidateStaticPeers bounds the list and refuses duplicates. A repeated
// fingerprint is rejected rather than merged: two endpoints claiming the same
// device is a configuration mistake the owner should see, and silently keeping
// one of them would hide which.
func ValidateStaticPeers(peers []StaticPeer) error {
	if len(peers) > MaxStaticPeers {
		return errors.New("at most 32 static peers may be configured")
	}
	seenFingerprint, seenEndpoint := map[string]bool{}, map[string]bool{}
	for _, p := range peers {
		if err := p.Validate(); err != nil {
			return err
		}
		if seenFingerprint[p.Fingerprint] {
			return errors.New("duplicate static peer fingerprint; one device gets one endpoint")
		}
		if seenEndpoint[p.Endpoint] {
			return errors.New("duplicate static peer endpoint")
		}
		seenFingerprint[p.Fingerprint], seenEndpoint[p.Endpoint] = true, true
	}
	return nil
}

// SortStaticPeers orders by fingerprint so the stored file and every listing are
// stable across edits, and so a diff of the config shows a real change rather
// than map ordering.
func SortStaticPeers(peers []StaticPeer) {
	sort.SliceStable(peers, func(i, j int) bool { return peers[i].Fingerprint < peers[j].Fingerprint })
}

// DecodeStaticPeer is the strict exactly-once decoder for one entry, following
// DecodeResourcePolicy's discipline: null, duplicate keys, unknown fields,
// empty strings and trailing JSON are all refused. Every value that reaches the
// stored config passes through here — the CLI encodes its flags and decodes them
// back through this function rather than building the struct directly, so there
// is one validation path and a future local-API PUT cannot get a weaker one.
//
// Backward compatibility: `label` is optional, so a payload written before it
// existed still decodes; endpoint and fingerprint are required, because an entry
// missing either is not half-configured, it is unusable.
func DecodeStaticPeer(body []byte) (StaticPeer, error) {
	var p StaticPeer
	texts := map[string]*string{"endpoint": &p.Endpoint, "fingerprint": &p.Fingerprint, "label": &p.Label}
	required := []string{"endpoint", "fingerprint"}
	d := json.NewDecoder(bytes.NewReader(body))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return p, errStaticPeer
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		target := texts[key]
		if err != nil || !ok || target == nil || seen[key] {
			return p, errStaticPeer
		}
		seen[key] = true
		// A pointer target separates an explicit null from an absent key; null
		// must not decode to the empty string and then to a default.
		var value *string
		if err := d.Decode(&value); err != nil || value == nil || *value == "" {
			return p, errStaticPeer
		}
		*target = *value
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return p, errStaticPeer
	}
	for _, key := range required {
		if !seen[key] {
			return p, errStaticPeer
		}
	}
	if d.Decode(new(any)) != io.EOF {
		return p, errStaticPeer
	}
	return p, p.Validate()
}

// AddStaticPeer returns the list with one entry added. It refuses a duplicate
// rather than replacing it: overwriting an endpoint for a fingerprint the owner
// already configured is the kind of silent change that makes a dial go somewhere
// unexpected, so the CLI asks them to remove it first.
func AddStaticPeer(peers []StaticPeer, add StaticPeer) ([]StaticPeer, error) {
	if err := add.Validate(); err != nil {
		return nil, err
	}
	out := append([]StaticPeer(nil), peers...)
	out = append(out, add)
	if err := ValidateStaticPeers(out); err != nil {
		return nil, err
	}
	SortStaticPeers(out)
	return out, nil
}

// RemoveStaticPeer removes by fingerprint, which is the stable identifier; an
// address may change, and removing by address would leave a pinned fingerprint
// configured with a stale endpoint.
func RemoveStaticPeer(peers []StaticPeer, fingerprint string) ([]StaticPeer, error) {
	if !validDeviceFingerprint(fingerprint) {
		return nil, errors.New("fingerprint must be 64 lowercase hex characters")
	}
	out := make([]StaticPeer, 0, len(peers))
	found := false
	for _, p := range peers {
		if p.Fingerprint == fingerprint {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return nil, errors.New("no static peer with that fingerprint is configured")
	}
	return out, nil
}
