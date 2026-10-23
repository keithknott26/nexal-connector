package inference

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
)

// AutoListen is the --also-listen value that means "every neXal and LAN address of this Mac".
const AutoListen = "auto"

// ShareAddresses finds this Mac's neXal addresses (100.64.0.0/10, the mesh's carrier-grade range) and its
// private LAN addresses (10/8, 172.16/12, 192.168/16) on up interfaces, skipping loopback, virtual machine
// bridges and container interfaces. Each list keeps the system's interface order (en0 before en1).
func ShareAddresses() (mesh, lan []string) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	for _, in := range ifs {
		if in.Flags&net.FlagUp == 0 || in.Flags&net.FlagLoopback != 0 {
			continue
		}
		virtual := false
		for _, p := range []string{"bridge", "vmenet", "docker", "veth", "anpi", "awdl", "llw"} {
			virtual = virtual || strings.HasPrefix(in.Name, p)
		}
		addrs, _ := in.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			switch {
			case ip4 == nil:
			case cgnat.Contains(ip4):
				mesh = append(mesh, ip4.String())
			case ip4.IsPrivate() && !virtual && !strings.HasPrefix(in.Name, "utun"):
				lan = append(lan, ip4.String())
			}
		}
	}
	return mesh, lan
}

// Relay accepts TCP connections on listen and joins each one to target, byte for byte, so one model server
// can answer on more than the single address it binds (loopback for this Mac, its neXal address for the
// owner's other Macs, its LAN address). Streaming answers pass through untouched. It stops when ctx ends.
func Relay(ctx context.Context, listen, target string) (net.Listener, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() {
		for {
			in, err := ln.Accept()
			if err != nil {
				return
			}
			go relayConn(ctx, in, target)
		}
	}()
	return ln, nil
}

func relayConn(ctx context.Context, in net.Conn, target string) {
	defer in.Close()
	var d net.Dialer
	out, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return
	}
	defer out.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	pipe := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}
	go pipe(out, in)
	go pipe(in, out)
	wg.Wait()
}

// ValidateAlsoListen checks extra addresses for `inference serve --also-listen`: each one specific IP
// (never 0.0.0.0 or ::), not the server's own host, and no repeats.
func ValidateAlsoListen(host string, extra []string) ([]string, error) {
	seen := map[string]bool{host: true}
	var out []string
	for _, a := range extra {
		ip := net.ParseIP(a)
		if ip == nil || ip.IsUnspecified() {
			return nil, errf("invalid_arguments", "pass specific IP addresses, for example this Mac's neXal or LAN address", "--also-listen must be one specific IP address, not %q", a)
		}
		s := ip.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}
