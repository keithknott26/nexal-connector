package wol

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
)

// Attempt is one interface a magic packet was sent through.
type Attempt struct {
	Interface   string `json:"interface"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	// Bound is true when the socket was bound to Source, so the packet leaves that
	// interface. False means the packet followed the routing table, which can pick a
	// different interface (a tunnel, say) than the one named.
	Bound   bool   `json:"bound"`
	Packets int    `json:"packets"`
	Limited bool   `json:"limited"`
	Error   string `json:"error,omitempty"`
}

// Skipped is an interface that was not used, with the reason.
type Skipped struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
	Reason    string `json:"reason"`
}

// Report describes everything one Send did, for diagnostics.
type Report struct {
	MACs     []string  `json:"macs"`
	Attempts []Attempt `json:"attempts"`
	Skipped  []Skipped `json:"skipped"`
}

// Interfaces is how many interfaces carried at least one directed broadcast.
func (r Report) Interfaces() int {
	n := 0
	for _, a := range r.Attempts {
		if !a.Limited && a.Packets > 0 {
			n++
		}
	}
	return n
}

// bindPerInterface sends each interface's packets from a socket bound to that interface's
// own address, so the broadcast leaves THAT interface instead of whichever one the routing
// table prefers. It is a variable so tests with fake interface tables can turn it off.
var bindPerInterface = true

var openUDPFrom = func(ip netip.Addr) (udpSender, error) {
	return net.ListenUDP("udp4", &net.UDPAddr{IP: ip.AsSlice()})
}

var (
	traceMu sync.Mutex
	traceFn func(Report)
)

// SetTrace registers a callback that receives the Report of every Send, successful or not.
func SetTrace(fn func(Report)) {
	traceMu.Lock()
	traceFn = fn
	traceMu.Unlock()
}

func emitTrace(r Report) {
	traceMu.Lock()
	fn := traceFn
	traceMu.Unlock()
	if fn != nil {
		fn(r)
	}
}

// MaskMAC hides the vendor-specific half of a hardware address for logs.
func MaskMAC(mac net.HardwareAddr) string {
	if len(mac) != 6 {
		return "invalid"
	}
	return fmt.Sprintf("xx:xx:xx:%02x:%02x:%02x", mac[3], mac[4], mac[5])
}

var limitedBroadcast = netip.AddrFrom4([4]byte{255, 255, 255, 255})

// SendDetailed broadcasts a magic packet for each MAC to UDP port 9 on the directed
// broadcast address of every usable IPv4 interface, and to the limited broadcast
// 255.255.255.255. With bindPerInterface each interface uses its own socket so the
// packet is sent from, and out of, that interface; the report says which.
func SendDetailed(macs []net.HardwareAddr) (Report, error) {
	var rep Report
	if len(macs) == 0 || len(macs) > MaxMACs {
		return rep, fmt.Errorf("wake needs 1 to %d MAC addresses", MaxMACs)
	}
	packets := make([][]byte, 0, len(macs))
	for _, m := range macs {
		p, err := BuildMagicPacket(m)
		if err != nil {
			return rep, err
		}
		packets = append(packets, p)
		rep.MACs = append(rep.MACs, MaskMAC(m))
	}
	ifaces, _, err := systemInterfaces()
	if err != nil {
		emitTrace(rep)
		return rep, errors.New("cannot enumerate network interfaces")
	}
	shared, err := openUDP()
	if err != nil {
		emitTrace(rep)
		return rep, errors.New("cannot open a UDP socket for Wake-on-LAN")
	}
	defer shared.Close()

	write := func(conn udpSender, dst netip.Addr) (int, string) {
		sent, firstErr := 0, ""
		for _, p := range packets {
			if _, err := conn.WriteToUDPAddrPort(p, netip.AddrPortFrom(dst, Port)); err != nil {
				if firstErr == "" {
					firstErr = err.Error()
				}
				continue
			}
			sent++
		}
		return sent, firstErr
	}

	seen := map[netip.Addr]bool{}
	anyBound := false
	limitedOK := false
	for _, in := range ifaces {
		if why := skipReason(in); why != "" {
			rep.Skipped = append(rep.Skipped, Skipped{Interface: in.Name, Address: in.Prefix.String(), Reason: why})
			continue
		}
		bc := broadcastOf(in.Prefix)
		if seen[bc] {
			rep.Skipped = append(rep.Skipped, Skipped{Interface: in.Name, Address: in.Prefix.String(), Reason: "same broadcast address as an interface already used"})
			continue
		}
		seen[bc] = true
		at := Attempt{Interface: in.Name, Source: in.Prefix.Addr().String(), Destination: bc.String()}
		conn := shared
		var own udpSender
		if bindPerInterface {
			if c, err := openUDPFrom(in.Prefix.Addr()); err == nil {
				own, conn = c, c
				at.Bound, anyBound = true, true
			}
		}
		at.Packets, at.Error = write(conn, bc)
		if at.Bound {
			// The limited broadcast leaves via the interface owning the bound source address.
			lim := Attempt{Interface: in.Name, Source: at.Source, Destination: limitedBroadcast.String(), Bound: true, Limited: true}
			lim.Packets, lim.Error = write(conn, limitedBroadcast)
			if lim.Packets > 0 {
				limitedOK = true
			}
			rep.Attempts = append(rep.Attempts, at, lim)
		} else {
			rep.Attempts = append(rep.Attempts, at)
		}
		if own != nil {
			own.Close()
		}
	}
	if !anyBound {
		lim := Attempt{Interface: "(routing table)", Destination: limitedBroadcast.String(), Limited: true}
		lim.Packets, lim.Error = write(shared, limitedBroadcast)
		limitedOK = lim.Packets > 0
		rep.Attempts = append(rep.Attempts, lim)
	}
	emitTrace(rep)
	if rep.Interfaces() == 0 && !limitedOK {
		return rep, errors.New("no network interface could send the Wake-on-LAN packet")
	}
	return rep, nil
}
