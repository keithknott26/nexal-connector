// Package wol builds and broadcasts Wake-on-LAN magic packets and reports what
// another Mac on the same local network needs in order to wake this one.
package wol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
)

// MaxMACs bounds the hardware addresses reported for this host, matching the
// coordinator's limit.
const MaxMACs = 8

// MagicPacket is 6 bytes of 0xFF followed by the target MAC repeated 16 times.
func MagicPacket(mac net.HardwareAddr) ([]byte, error) {
	if len(mac) != 6 {
		return nil, errors.New("wake-on-LAN needs a 6-byte hardware address")
	}
	p := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		p = append(p, 0xff)
	}
	for i := 0; i < 16; i++ {
		p = append(p, mac...)
	}
	return p, nil
}

// sendTo is replaced in tests.
var sendTo = func(packet []byte, addr string) error {
	conn, err := net.Dial("udp4", addr) // Go enables SO_BROADCAST on UDP/IPv4 sockets.
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(packet)
	return err
}

// iface is the part of a network interface this package reads.
type iface struct {
	Name  string
	Flags net.Flags
	MAC   net.HardwareAddr
	Addrs []netip.Prefix
}

// localInterfaces is replaced in tests.
var localInterfaces = func() ([]iface, error) {
	list, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]iface, 0, len(list))
	for _, ifc := range list {
		entry := iface{Name: ifc.Name, Flags: ifc.Flags, MAC: ifc.HardwareAddr}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				entry.Addrs = append(entry.Addrs, p)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// virtualPrefixes name interfaces that never face the physical LAN: VM and
// container bridges, tunnels and Apple peer-to-peer links. Their private
// subnets (for example a VM bridge's 192.168.64.0/24) differ between Macs on
// the same network, so counting them would split one LAN into many.
var virtualPrefixes = []string{"bridge", "vmnet", "vboxnet", "docker", "veth", "utun", "tun", "tap", "awdl", "llw", "anpi", "ap", "gif", "stf", "lo"}

func physical(name string) bool {
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// lanInterface pairs an eligible interface with its private IPv4 prefixes.
type lanInterface struct {
	mac      net.HardwareAddr
	prefixes []netip.Prefix
}

// lanInterfaces returns the up, broadcast-capable, non-loopback,
// non-point-to-point physical interfaces that carry at least one private
// (RFC 1918) IPv4 address. 100.64.0.0/10 (the secure network) and
// 169.254.0.0/16 (link-local) are not private by this definition.
func lanInterfaces() ([]lanInterface, error) {
	list, err := localInterfaces()
	if err != nil {
		return nil, err
	}
	var out []lanInterface
	for _, ifc := range list {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 || ifc.Flags&net.FlagLoopback != 0 ||
			ifc.Flags&net.FlagPointToPoint != 0 || !physical(ifc.Name) {
			continue
		}
		var prefixes []netip.Prefix
		for _, p := range ifc.Addrs {
			a := p.Addr().Unmap()
			if !a.Is4() || !a.IsPrivate() || p.Bits() < 8 || p.Bits() > 30 {
				continue
			}
			prefixes = append(prefixes, netip.PrefixFrom(a, p.Bits()))
		}
		if len(prefixes) > 0 {
			out = append(out, lanInterface{mac: ifc.MAC, prefixes: prefixes})
		}
	}
	return out, nil
}

// WakeInterfaces returns this Mac's wakeable hardware addresses (lowercase,
// sorted, unicast, at most MaxMACs) and its LAN key: the hex SHA-256 of its
// sorted, deduplicated private IPv4 network prefixes (for example
// "192.168.68.0/22") joined with "\n". Both are empty when this Mac has no
// private IPv4 network or no usable hardware address.
func WakeInterfaces() (macs []string, lanKey string) {
	list, err := lanInterfaces()
	if err != nil {
		return nil, ""
	}
	var nets []string
	for _, ifc := range list {
		if m := unicastMAC(ifc.mac); m != "" {
			macs = append(macs, m)
		}
		for _, p := range ifc.prefixes {
			nets = append(nets, p.Masked().String())
		}
	}
	slices.Sort(macs)
	macs = slices.Compact(macs)
	if len(macs) > MaxMACs {
		macs = macs[:MaxMACs]
	}
	if len(macs) == 0 || len(nets) == 0 {
		return nil, ""
	}
	slices.Sort(nets)
	nets = slices.Compact(nets)
	sum := sha256.Sum256([]byte(strings.Join(nets, "\n")))
	return macs, hex.EncodeToString(sum[:])
}

// unicastMAC renders a 6-byte, non-zero, non-group hardware address.
func unicastMAC(hw net.HardwareAddr) string {
	if len(hw) != 6 || hw[0]&1 != 0 {
		return ""
	}
	for _, b := range hw {
		if b != 0 {
			return strings.ToLower(hw.String())
		}
	}
	return ""
}

// broadcastTargets is the limited broadcast plus every eligible interface's
// subnet broadcast, deduplicated.
func broadcastTargets() []string {
	targets := []string{"255.255.255.255"}
	list, _ := lanInterfaces()
	for _, ifc := range list {
		for _, p := range ifc.prefixes {
			if b := broadcastOf(p); !slices.Contains(targets, b) {
				targets = append(targets, b)
			}
		}
	}
	return targets
}

// SendAll broadcasts a magic packet for each MAC to 255.255.255.255 and to the
// subnet broadcast of every eligible local interface, on UDP ports 9 and 7. It
// never addresses the target directly. Invalid MACs are skipped; it fails
// only when no MAC is valid or no packet could be sent at all.
func SendAll(macs []string) error {
	var packets [][]byte
	for _, m := range macs {
		hw, err := net.ParseMAC(m)
		if err != nil || unicastMAC(hw) == "" {
			continue
		}
		p, err := MagicPacket(hw)
		if err != nil {
			continue
		}
		packets = append(packets, p)
	}
	if len(packets) == 0 {
		return errors.New("no valid hardware address to wake")
	}
	targets := broadcastTargets()
	var sent bool
	var last error
	for _, packet := range packets {
		for _, t := range targets {
			for _, port := range []string{"9", "7"} {
				if err := sendTo(packet, net.JoinHostPort(t, port)); err != nil {
					last = err
					continue
				}
				sent = true
			}
		}
	}
	if !sent {
		if last == nil {
			last = errors.New("no destination")
		}
		return errors.New("magic packet could not be sent: " + last.Error())
	}
	return nil
}

func broadcastOf(p netip.Prefix) string {
	a := p.Addr().As4()
	bits := p.Bits()
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	if bits < 32 {
		v |= (1 << (32 - bits)) - 1
	}
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}).String()
}

// parseWakeOnMagicPacket reads `pmset -g custom` output: true only when at
// least one "womp" line exists and every one of them is 1 (the setting is
// per power source, and a Mac that sleeps unreachable on battery should not
// be advertised as wakeable).
func parseWakeOnMagicPacket(out string) bool {
	found := false
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "womp" {
			continue
		}
		if len(f) != 2 || f[1] != "1" {
			return false
		}
		found = true
	}
	return found
}
