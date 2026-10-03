package wol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"regexp"
	"slices"
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
	// PrefixKeys are per-prefix fingerprints (see PrefixKeys), so the
	// coordinator can match two Macs that share one LAN even when their full
	// LANKey or public address differs.
	PrefixKeys []string
	// WakeForNetwork is the macOS "Wake for network access" setting (pmset
	// womp): WakeEnabled, WakeDisabled, or WakeUnknown off macOS or when pmset
	// cannot be read.
	WakeForNetwork string
}

// LANKey is hex(sha256(<sorted, deduplicated IPv4 prefixes joined by ",">)).
//
// WHY THIS SHAPE. Two Macs can relay a wake to each other only if a broadcast
// from one reaches the other, i.e. they share a layer-2 segment. The local
// IPv4 network prefix is the best cheap proxy for that. The prefixes are sorted
// so interface enumeration order cannot change the key, and hashed so the
// coordinator stores an equality token rather than this Mac's internal
// addressing plan.
//
// The key alone is NOT unique — half the homes on earth are 192.168.1.0/24 —
// and deliberately so: the coordinator salts it with the source IP it observes
// on the PUT /api/v2/hosts/wake-info request, which is what separates one
// household's 192.168.1.0/24 from the neighbour's. The connector does not mix
// in a public address itself, because the only one it could know is the one
// peer discovery learns after an advertise, and two Macs on one LAN that
// differed in whether discovery had run would then disagree on the key.
//
// A multi-homed Mac whose interfaces straddle two LANs puts all its prefixes in
// one key, so it matches neither LAN's single-homed Macs; that is a known,
// accepted limitation.
// PrefixKeys returns hex(sha256("nexal-lan-prefix|" + prefix)) for each
// distinct prefix, sorted. Unlike LANKey these match on ANY shared prefix, which
// survives a Mac with an extra physical network or one whose coordinator traffic
// leaves through a different public address (a neXal exit, IPv6, a VPN). The
// coordinator only compares them within one tenant.
func PrefixKeys(prefixes []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range prefixes {
		sum := sha256.Sum256([]byte("nexal-lan-prefix|" + p))
		k := hex.EncodeToString(sum[:])
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	if len(out) > MaxPrefixKeys {
		out = out[:MaxPrefixKeys]
	}
	return out
}

// MaxPrefixKeys is the coordinator's limit on reported prefix fingerprints.
const MaxPrefixKeys = 16

func LANKey(prefixes []string) string {
	if len(prefixes) == 0 {
		return ""
	}
	sorted := append([]string(nil), prefixes...)
	sort.Strings(sorted)
	sorted = slices.Compact(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(sum[:])
}

// CollectLocal enumerates the physical interfaces and reads the wake setting.
// It never fails: every fact it cannot establish is reported empty or unknown.
func CollectLocal(ctx context.Context) Facts {
	f := Facts{MACs: []string{}, Prefixes: []string{}, WakeForNetwork: wakeForNetwork(ctx)}
	v4, ifs, err := systemInterfaces()
	if err != nil {
		return f
	}
	// lanPort: a real network port (en*), even one using a private (locally
	// administered) Wi-Fi address. Its prefix identifies the LAN, so this Mac can
	// RELAY wakes; only a globally unique MAC can be a wake TARGET.
	lanPort := map[string]bool{}
	macSet := map[string]bool{}
	for _, in := range ifs {
		if in.Flags&net.FlagLoopback != 0 || isVirtual(in.Name) || len(in.HardwareAddr) != 6 {
			continue
		}
		lanPort[in.Name] = true
		if physicalMAC(in.HardwareAddr) {
			macSet[strings.ToLower(in.HardwareAddr.String())] = true
		}
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
		if !lanPort[in.Name] || in.Flags&net.FlagUp == 0 || !a.Is4() || a.IsLoopback() || a.IsLinkLocalUnicast() {
			continue
		}
		prefixSet[in.Prefix.Masked().String()] = true
	}
	for p := range prefixSet {
		f.Prefixes = append(f.Prefixes, p)
	}
	sort.Strings(f.Prefixes)
	f.LANKey = LANKey(f.Prefixes)
	f.PrefixKeys = PrefixKeys(f.Prefixes)
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
