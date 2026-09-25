// Package wol builds and broadcasts Wake-on-LAN magic packets and reports the
// local interface another Mac would need in order to wake this one.
package wol

import (
	"errors"
	"net"
	"net/netip"
	"strings"
)

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

// Send broadcasts the magic packet for mac on UDP 9 and 7, to the given subnet
// broadcast (when valid and private) and to the limited broadcast address.
// It succeeds if any destination accepted the packet.
func Send(mac, broadcast string) error {
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return errors.New("invalid hardware address")
	}
	packet, err := MagicPacket(hw)
	if err != nil {
		return err
	}
	targets := []string{"255.255.255.255"}
	if b, err := netip.ParseAddr(broadcast); err == nil && b.Is4() && b.IsPrivate() {
		targets = append([]string{b.String()}, targets...)
	}
	var sent bool
	var last error
	for _, t := range targets {
		for _, port := range []string{"9", "7"} {
			if err := sendTo(packet, net.JoinHostPort(t, port)); err != nil {
				last = err
				continue
			}
			sent = true
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

// Interface is what another host needs to wake this one.
type Interface struct {
	MAC       string
	Broadcast string
}

// routeLocalAddr is replaced in tests. It returns the source address the OS
// would use for Internet traffic; no packet is sent.
var routeLocalAddr = func() (netip.Addr, error) {
	conn, err := net.Dial("udp4", "1.1.1.1:80")
	if err != nil {
		return netip.Addr{}, err
	}
	defer conn.Close()
	ap, err := netip.ParseAddrPort(conn.LocalAddr().String())
	return ap.Addr(), err
}

var interfaces = net.Interfaces

// Local returns the hardware address and IPv4 broadcast of the interface that
// carries the default route, provided it is a broadcast-capable Ethernet-style
// interface on a private network.
func Local() (Interface, bool) {
	src, err := routeLocalAddr()
	if err != nil || !src.Is4() || !src.IsPrivate() {
		return Interface{}, false
	}
	list, err := interfaces()
	if err != nil {
		return Interface{}, false
	}
	for _, ifc := range list {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagBroadcast == 0 || ifc.Flags&net.FlagLoopback != 0 ||
			ifc.Flags&net.FlagPointToPoint != 0 || len(ifc.HardwareAddr) != 6 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			prefix, err := netip.ParsePrefix(a.String())
			if err != nil || prefix.Addr() != src {
				continue
			}
			return Interface{MAC: strings.ToLower(ifc.HardwareAddr.String()), Broadcast: broadcastOf(prefix)}, true
		}
	}
	return Interface{}, false
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
