// Package wol sends Wake-on-LAN magic packets and gathers the local facts the
// coordinator needs to route a wake request to a Mac on the right network.
//
// Wake-on-LAN cannot cross a router: a magic packet is a LAN broadcast, and a
// sleeping NIC only listens on its own segment. So waking a Mac remotely means
// the coordinator must ask an AWAKE neXal Mac on the same LAN to send the
// packet. This package supplies both halves of that:
//
//   - Send / BuildMagicPacket: the sending half, used by `nexal wake --mac` and
//     by the agent when the coordinator relays a wake.request to it.
//   - CollectLocal: this Mac's physical MAC addresses (so it can BE woken), a
//     lanKey (so the coordinator can tell which awake Macs share its LAN), and
//     the macOS "Wake for network access" setting (so the owner can see why a
//     wake would not work).
//
// Nothing here authorizes anything. A magic packet wakes a NIC; it carries no
// credential and grants no access, which is why no signature is attached.
package wol

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Port is the conventional discard port for magic packets. Port 7 and 9 are
// both used in the wild; 9 is what macOS's own sleep proxy and most tools use.
const Port = 9

// MaxMACs bounds how many addresses one wake (or one wake-info report) may
// carry. A Mac has at most a handful of physical interfaces; the bound keeps a
// hostile wake.request from turning this host into a packet amplifier.
const MaxMACs = 8

// ParseMAC parses exactly six octets written as aa:bb:cc:dd:ee:ff or
// aa-bb-cc-dd-ee-ff (hex, either case, one separator style throughout). It is
// deliberately stricter than net.ParseMAC, which also accepts dotted Cisco
// notation and 8- and 20-byte EUI/InfiniBand forms that a magic packet cannot
// carry. The all-zero address and any group (multicast/broadcast) address are
// refused: neither can name a single NIC.
func ParseMAC(s string) (net.HardwareAddr, error) {
	if len(s) != 17 {
		return nil, errors.New("invalid MAC address: want aa:bb:cc:dd:ee:ff")
	}
	sep := s[2]
	if sep != ':' && sep != '-' {
		return nil, errors.New("invalid MAC address: want aa:bb:cc:dd:ee:ff")
	}
	mac := make(net.HardwareAddr, 6)
	for i := 0; i < 6; i++ {
		if i > 0 && s[i*3-1] != sep {
			return nil, errors.New("invalid MAC address: mixed or missing separators")
		}
		hi, ok1 := hexNibble(s[i*3])
		lo, ok2 := hexNibble(s[i*3+1])
		if !ok1 || !ok2 {
			return nil, errors.New("invalid MAC address: non-hex digit")
		}
		mac[i] = hi<<4 | lo
	}
	if err := checkUnicast(mac); err != nil {
		return nil, err
	}
	return mac, nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func checkUnicast(mac net.HardwareAddr) error {
	if len(mac) != 6 {
		return errors.New("invalid MAC address: want six octets")
	}
	if mac[0]&0x01 != 0 {
		return errors.New("invalid MAC address: multicast or broadcast address cannot be woken")
	}
	zero := true
	for _, b := range mac {
		if b != 0 {
			zero = false
		}
	}
	if zero {
		return errors.New("invalid MAC address: all-zero address")
	}
	return nil
}

// BuildMagicPacket returns the 102-byte magic packet for mac: six 0xFF bytes
// followed by the MAC repeated sixteen times. No SecureOn password is appended;
// Apple hardware does not support one.
func BuildMagicPacket(mac net.HardwareAddr) ([]byte, error) {
	if err := checkUnicast(mac); err != nil {
		return nil, err
	}
	p := make([]byte, 0, 6+16*6)
	for i := 0; i < 6; i++ {
		p = append(p, 0xFF)
	}
	for i := 0; i < 16; i++ {
		p = append(p, mac...)
	}
	return p, nil
}

// ifaceV4 is one interface's IPv4 configuration, extracted so the broadcast
// computation is testable without the host's real interfaces.
type ifaceV4 struct {
	Name   string
	Flags  net.Flags
	Prefix netip.Prefix // address/length, NOT masked
}

// directedBroadcasts returns one directed broadcast address per usable
// interface, deduplicated, in input order. Usable means up, broadcast-capable,
// not loopback, not point-to-point, not a virtual interface (see isVirtual),
// not link-local (169.254/16 is a self-assigned address, not a LAN anyone else
// is on), and a prefix of /30 or shorter (a /31 or /32 has no broadcast).
func directedBroadcasts(ifaces []ifaceV4) []netip.Addr {
	var out []netip.Addr
	seen := map[netip.Addr]bool{}
	for _, in := range ifaces {
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagBroadcast == 0 ||
			in.Flags&net.FlagLoopback != 0 || in.Flags&net.FlagPointToPoint != 0 || isVirtual(in.Name) {
			continue
		}
		a := in.Prefix.Addr()
		if !a.Is4() || a.IsLoopback() || a.IsLinkLocalUnicast() || in.Prefix.Bits() < 0 || in.Prefix.Bits() > 30 {
			continue
		}
		b := a.As4()
		host := uint32(0xFFFFFFFF) >> uint(in.Prefix.Bits())
		v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		v |= host
		bc := netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
		if !seen[bc] {
			seen[bc] = true
			out = append(out, bc)
		}
	}
	return out
}

// isVirtual names the macOS interface families that are never the physical
// port a sleeping Mac listens on: tunnels (utun, gif, stf, ipsec, ppp),
// Apple Wireless Direct Link and its low-latency sibling (awdl, llw), bridges
// (bridge, including Thunderbolt Bridge and Internet Sharing), loopback (lo),
// WireGuard-style interfaces (wt*), the Apple-silicon internal NCM link to the
// Studio Display and similar (anpi), Wi-Fi access-point mode (ap), and the
// virtualization framework's shims (vmenet, feth). Physical Ethernet and Wi-Fi
// are en*; anything unknown that survives this list is still filtered by the
// MAC checks in physicalMAC.
func isVirtual(name string) bool {
	for _, p := range []string{"utun", "awdl", "llw", "bridge", "lo", "wt", "anpi", "ap",
		"gif", "stf", "ipsec", "ppp", "vmenet", "feth", "tun", "tap"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// physicalMAC reports whether hw is a usable, globally unique MAC: six octets,
// unicast, non-zero, and NOT locally administered. The locally-administered bit
// (0x02) is set on every randomized/private Wi-Fi address and on virtual
// adapters; a coordinator told to wake such an address would be told to wake
// something that does not exist while the Mac sleeps.
func physicalMAC(hw net.HardwareAddr) bool {
	return checkUnicast(hw) == nil && hw[0]&0x02 == 0
}

// systemInterfaces reads the host's IPv4 interface configuration. It is a
// variable so tests can substitute a fixed table.
var systemInterfaces = func() ([]ifaceV4, []net.Interface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	var out []ifaceV4
	for _, in := range ifs {
		addrs, err := in.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			if !ip.Is4() {
				continue
			}
			ones, bits := ipn.Mask.Size()
			if bits != 32 {
				continue
			}
			out = append(out, ifaceV4{Name: in.Name, Flags: in.Flags, Prefix: netip.PrefixFrom(ip, ones)})
		}
	}
	return out, ifs, nil
}

// udpSender is the one network operation, injectable for tests.
type udpSender interface {
	WriteToUDPAddrPort([]byte, netip.AddrPort) (int, error)
	Close() error
}

var openUDP = func() (udpSender, error) {
	// Go sets SO_BROADCAST on every UDP socket it creates (setDefaultSockopts),
	// so no raw syscall is needed for the broadcast destinations below.
	return net.ListenUDP("udp4", nil)
}

// Send broadcasts a magic packet for each MAC to UDP port 9 on the directed
// broadcast address of every usable IPv4 interface, and to the limited
// broadcast 255.255.255.255 (which macOS sends out of the primary interface
// only; the directed broadcasts are what reach the other segments this Mac is
// attached to).
//
// It returns how many interfaces at least one packet was sent through. Zero
// with a nil error means only the limited broadcast went out. An error is
// returned only when nothing at all could be sent.
func Send(macs []net.HardwareAddr) (int, error) {
	if len(macs) == 0 || len(macs) > MaxMACs {
		return 0, fmt.Errorf("wake needs 1 to %d MAC addresses", MaxMACs)
	}
	packets := make([][]byte, 0, len(macs))
	for _, m := range macs {
		p, err := BuildMagicPacket(m)
		if err != nil {
			return 0, err
		}
		packets = append(packets, p)
	}
	ifaces, _, err := systemInterfaces()
	if err != nil {
		return 0, errors.New("cannot enumerate network interfaces")
	}
	conn, err := openUDP()
	if err != nil {
		return 0, errors.New("cannot open a UDP socket for Wake-on-LAN")
	}
	defer conn.Close()
	interfaces := 0
	for _, bc := range directedBroadcasts(ifaces) {
		ok := false
		for _, p := range packets {
			if _, err := conn.WriteToUDPAddrPort(p, netip.AddrPortFrom(bc, Port)); err == nil {
				ok = true
			}
		}
		if ok {
			interfaces++
		}
	}
	limited := false
	for _, p := range packets {
		if _, err := conn.WriteToUDPAddrPort(p, netip.AddrPortFrom(netip.AddrFrom4([4]byte{255, 255, 255, 255}), Port)); err == nil {
			limited = true
		}
	}
	if interfaces == 0 && !limited {
		return 0, errors.New("no network interface could send the Wake-on-LAN packet")
	}
	return interfaces, nil
}
