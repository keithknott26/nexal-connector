package inference

import (
	"context"
	"io"
	"net"
	"sync"
)

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
