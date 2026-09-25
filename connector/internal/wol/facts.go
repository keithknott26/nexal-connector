package wol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

// Wake-for-network states reported in status and to the coordinator.
const (
	WakeEnabled  = "enabled"
	WakeDisabled = "disabled"
	WakeUnknown  = "unknown"
)

// Facts is what this Mac knows about its own wakeability.
type Facts struct {
	// MACs are this Mac's physical Ethernet/Wi-Fi addresses, lowercase
	// colon-separated, sorted, at most MaxMACs. They are what a relay puts in a
	// magic packet to wake THIS Mac.
	MACs []string
	// Prefixes are the masked IPv4 networks (e.g. "192.168.1.0/24") of the up
	// interfaces that own those MACs, sorted and deduplicated. They are inputs
	// to LANKey and are not reported on their own.
	Prefixes []string
	// LANKey identifies "the same LAN" without revealing its addressing; see
	// LANKey. Empty when this Mac has no usable IPv4 network right now.
	LANKey string
	// WakeForNetwork is the macOS "Wake for network access" setting (pmset
	// womp): WakeEnabled, WakeDisabled, or WakeUnknown off macOS or when pmset
	// cannot be read.
	WakeForNetwork string
}

// LANKey is hex(sha256(<sorted prefixes joined by ","> "|" <public IP>)), or
// hex(sha256(<sorted prefixes joined by ",">)) when no public IP is known.
//
// WHY THIS SHAPE. Two Macs can relay a wake to each other only if a broadcast
// from one reaches the other, i.e. they share a layer-2 segment. The local
// IPv4 network prefix is the best cheap proxy for that. It is not unique on its
// own — half the homes on earth are 192.168.1.0/24 — so the public address the
// coordinator saw is mixed in to separate one household's 192.168.1.0/24 from
// the neighbour's. The prefixes are sorted so interface enumeration order
// cannot change the key, and the value is hashed so the coordinator stores an
// equality token rather than this Mac's internal addressing plan.
//
// KNOWN LIMITATION, stated rather than hidden: the public IP is only known to
// an agent whose peer discovery has completed an advertise (the coordinator's
// observed WAN address rides that response). Two Macs on the same LAN where one
// knows its public IP and the other does not will compute DIFFERENT keys, and
// the coordinator will not pair them as relays. The same happens across a
// multi-homed Mac whose interfaces straddle two LANs (all its prefixes are in
// one key). The robust fix is coordinator-side — key on the request's own
// observed source address — and is noted for the coordinator owner.
func LANKey(prefixes []string, publicIP string) string {
	if len(prefixes) == 0 {
		return ""
	}
	sorted := append([]string(nil), prefixes...)
	sort.Strings(sorted)
	material := strings.Join(sorted, ",")
	if ip, err := netip.ParseAddr(publicIP); err == nil && ip.IsGlobalUnicast() && !ip.IsPrivate() {
		material += "|" + ip.Unmap().String()
	}
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

// CollectLocal enumerates the physical interfaces and reads the wake setting.
// publicIP is the coordinator-observed address when the caller has one, or "".
// It never fails: every fact it cannot establish is reported empty or unknown.
func CollectLocal(ctx context.Context, publicIP string) Facts {
	f := Facts{MACs: []string{}, Prefixes: []string{}, WakeForNetwork: wakeForNetwork(ctx)}
	v4, ifs, err := systemInterfaces()
	if err != nil {
		return f
	}
	physical := map[string]bool{}
	macSet := map[string]bool{}
	for _, in := range ifs {
		if in.Flags&net.FlagLoopback != 0 || isVirtual(in.Name) || !physicalMAC(in.HardwareAddr) {
			continue
		}
		physical[in.Name] = true
		macSet[strings.ToLower(in.HardwareAddr.String())] = true
	}
	for m := range macSet {
		f.MACs = append(f.MACs, m)
	}
	sort.Strings(f.MACs)
	if len(f.MACs) > MaxMACs {
		f.MACs = f.MACs[:MaxMACs]
	}
	prefixSet := map[string]bool{}
	for _, in := range v4 {
		a := in.Prefix.Addr()
		if !physical[in.Name] || in.Flags&net.FlagUp == 0 || !a.Is4() || a.IsLoopback() || a.IsLinkLocalUnicast() {
			continue
		}
		prefixSet[in.Prefix.Masked().String()] = true
	}
	for p := range prefixSet {
		f.Prefixes = append(f.Prefixes, p)
	}
	sort.Strings(f.Prefixes)
	f.LANKey = LANKey(f.Prefixes, publicIP)
	return f
}

var wompRE = regexp.MustCompile(`(?m)^\s*womp\s+([01])\s*$`)

// ParseWomp reads the "womp" line of `pmset -g` output. Exactly one line of
// the exact shape " womp   1" is believed; zero lines (a Mac without the
// setting, e.g. some laptops on battery) or several is WakeUnknown rather than
// a guess.
func ParseWomp(b []byte) string {
	if len(b) > 256<<10 {
		return WakeUnknown
	}
	m := wompRE.FindAllSubmatch(b, -1)
	if len(m) != 1 {
		return WakeUnknown
	}
	if string(m[0][1]) == "1" {
		return WakeEnabled
	}
	return WakeDisabled
}
