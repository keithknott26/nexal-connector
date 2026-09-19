package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"
)

// NetworkAddress describes an assigned address, not a tested peer route, link
// speed, physical topology or trusted-LAN attestation.
type NetworkAddress struct {
	IP             string `json:"ip"`
	Interface      string `json:"interface"`
	ConnectionType string `json:"connectionType"`
	HardwarePort   string `json:"hardwarePort,omitempty"`
}

func (a NetworkAddress) Label() string {
	label := fmt.Sprintf("%s | %s | %s", a.IP, a.ConnectionType, a.Interface)
	if a.HardwarePort != "" {
		label += " | " + a.HardwarePort
	}
	return label
}

// boundedOutput limits both stdout and stderr of the local metadata command.
type boundedOutput struct{ buffer bytes.Buffer }

func (b *boundedOutput) Len() int       { return b.buffer.Len() }
func (b *boundedOutput) String() string { return b.buffer.String() }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 65536 {
		return 0, errors.New("hardware-port output too large")
	}
	return b.buffer.Write(p)
}

func cleanLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	r := []rune(strings.TrimSpace(s))
	if len(r) > 120 {
		r = r[:120]
	}
	return string(r)
}

func parseHardwarePorts(text string) map[string]string {
	ports := map[string]string{}
	port := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Hardware Port:") {
			port = cleanLabel(strings.TrimPrefix(line, "Hardware Port:"))
		} else if strings.HasPrefix(line, "Device:") {
			device := strings.TrimSpace(strings.TrimPrefix(line, "Device:"))
			if port != "" && device != "" && len(device) <= 32 && !strings.ContainsAny(device, " \t\r\n") {
				ports[device] = port
			}
			port = ""
		} else if line == "" {
			port = ""
		}
	}
	return ports
}

func hardwarePorts() map[string]string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/networksetup", "-listallhardwareports")
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	var out, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if cmd.Run() != nil {
		// Address discovery remains usable; do not infer Wi-Fi from en0/en1.
		return nil
	}
	return parseHardwarePorts(out.String())
}

func connectionType(device, port string) string {
	p := strings.ToLower(port)
	switch {
	case strings.HasPrefix(device, "utun"), strings.HasPrefix(device, "tun"), strings.HasPrefix(device, "tap"),
		strings.HasPrefix(device, "ppp"), strings.HasPrefix(device, "ipsec"):
		return "VPN/tunnel (not verified LAN)"
	case strings.HasPrefix(device, "awdl"), strings.HasPrefix(device, "llw"):
		return "Apple peer-to-peer (not ordinary Wi-Fi LAN)"
	case strings.Contains(p, "wi-fi"), strings.Contains(p, "wifi"), strings.Contains(p, "airport"),
		strings.Contains(p, "wireless lan"), strings.Contains(p, "wlan"):
		return "Wi-Fi"
	case strings.Contains(p, "thunderbolt") && strings.Contains(p, "bridge"):
		return "Thunderbolt bridge"
	case strings.Contains(p, "ethernet"), strings.Contains(p, "lan"):
		return "Ethernet (wired)"
	case strings.HasPrefix(device, "bridge"), strings.HasPrefix(device, "vmenet"):
		return "Bridge/virtual (type unverified)"
	default:
		return "Unknown connection type"
	}
}

func collectNetworks(interfaces []net.Interface, addrs func(net.Interface) ([]net.Addr, error), ports map[string]string) ([]NetworkAddress, error) {
	result := []NetworkAddress{}
	seen := map[string]bool{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := addrs(iface)
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || !ip.IsPrivate() {
				continue
			}
			key := iface.Name + "/" + ip.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			port := ports[iface.Name]
			result = append(result, NetworkAddress{IP: ip.String(), Interface: cleanLabel(iface.Name),
				ConnectionType: connectionType(iface.Name, port), HardwarePort: port})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Interface != result[j].Interface {
			return result[i].Interface < result[j].Interface
		}
		return result[i].IP < result[j].IP
	})
	return result, nil
}

func NetworkAddresses() ([]NetworkAddress, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	return collectNetworks(interfaces, func(i net.Interface) ([]net.Addr, error) { return i.Addrs() }, hardwarePorts())
}

// EndpointConnection is local-only. A remote IP cannot identify the remote
// machine's Wi-Fi/Ethernet hardware without separately supplied evidence.
func EndpointConnection(endpoint string) string {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "Unknown connection type"
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return "Loopback (same-machine test; not Ethernet or Wi-Fi)"
	}
	addresses, err := NetworkAddresses()
	if err == nil {
		var labels []string
		for _, a := range addresses {
			if ip != nil && ip.Equal(net.ParseIP(a.IP)) {
				labels = append(labels, a.Label())
			}
		}
		if len(labels) > 0 {
			return strings.Join(labels, "; ")
		}
	}
	return "Unknown connection type (interface metadata unavailable)"
}
