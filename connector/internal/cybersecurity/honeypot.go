package cybersecurity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Honeypot opens fake network services on this computer. Nothing legitimate
// should ever connect to them, so any connection from another computer is
// reported as an alert. It never runs commands, accepts logins, stores or
// transmits received bytes, or forwards traffic. It binds only this Mac's
// neXal network and private-LAN addresses (never a public address), and ignores
// loopback connections (this Mac talking to itself).
type Honeypot struct {
	Directory string
	// Test seams: nil/false in production.
	services      []HoneypotService
	allowLoopback bool
}

func HoneypotForConfig(path string) Honeypot { return Honeypot{Directory: filepath.Dir(path)} }

type HoneypotService struct {
	Name   string
	Port   int
	Banner []byte
}

// DefaultHoneypotServices use unprivileged ports that do not collide with real
// macOS services (Screen Sharing 5900, File Sharing 445, SSH 22 stay untouched).
var DefaultHoneypotServices = []HoneypotService{
	{"ssh", 2222, []byte("SSH-2.0-OpenSSH_9.6\r\n")},
	{"telnet", 2323, []byte("\r\nlogin: ")},
	{"rdp", 3389, nil},
	{"smb", 4445, nil},
	{"vnc", 5909, []byte("RFB 003.008\n")},
	{"http", 8081, []byte("HTTP/1.1 401 Unauthorized\r\nWWW-Authenticate: Basic realm=\"admin\"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")},
}

const (
	MaxHoneypotPending    = 20
	MaxHoneypotRecent     = 20
	honeypotHourlyEvents  = 20
	honeypotSourceCooloff = time.Hour
	honeypotHold          = 5 * time.Second
	honeypotReadLimit     = 512
	honeypotConcurrency   = 16
	honeypotTick          = 15 * time.Second
	honeypotCountCap      = 1_000_000
)

type HoneypotPort struct {
	Port      int    `json:"port"`
	Service   string `json:"service"`
	Listening bool   `json:"listening"`
	Error     string `json:"error,omitempty"`
}

// HoneypotConnection stays on this computer; only the Event leaves it.
type HoneypotConnection struct {
	ObservedAt    string `json:"observedAt"`
	Service       string `json:"service"`
	Port          int    `json:"port"`
	SourceAddress string `json:"sourceAddress"`
	SourceClass   string `json:"sourceClass"`
}

type HoneypotState struct {
	Enabled         bool                 `json:"enabled"`
	Status          string               `json:"status"`
	Ports           []HoneypotPort       `json:"ports,omitempty"`
	Triggers        int                  `json:"triggers"`
	LastTriggeredAt string               `json:"lastTriggeredAt,omitempty"`
	Pending         []Event              `json:"pending,omitempty"`
	Recent          []HoneypotConnection `json:"recent,omitempty"`
	// Connections not turned into alerts (hourly cap, per-source cool-off, full
	// outbox, or older than the server's acceptance window). Bounded.
	Suppressed int `json:"suppressed"`
}

func (h Honeypot) serviceList() []HoneypotService {
	if h.services != nil {
		return h.services
	}
	return DefaultHoneypotServices
}

func (h Honeypot) locked(fn func(*os.Root, *HoneypotState) error) error {
	parent, err := os.OpenRoot(h.Directory)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := parent.Mkdir("security-honeypot", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	stat, err := parent.Lstat("security-honeypot")
	if err != nil || !stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 || stat.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe honeypot directory")
	}
	root, err := parent.OpenRoot("security-honeypot")
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := root.OpenFile("lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if stat, err := lock.Stat(); err != nil || !stat.Mode().IsRegular() {
		return errors.New("unsafe honeypot lock")
	}
	// Short waits only: the agent holds the lock for milliseconds.
	for attempt := 0; ; attempt++ {
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		}
		if attempt == 20 {
			return errors.New("honeypot state busy")
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	state := HoneypotState{Status: "disabled"}
	f, err := root.OpenFile("state.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err == nil {
		defer f.Close()
		if stat, err := f.Stat(); err != nil || !stat.Mode().IsRegular() {
			return errors.New("unsafe honeypot state")
		}
		b, e := io.ReadAll(io.LimitReader(f, 65537))
		if e != nil || len(b) > 65536 || json.Unmarshal(b, &state) != nil {
			return errors.New("invalid honeypot state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fn(root, &state)
}

func saveHoneypot(root *os.Root, state *HoneypotState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	name := "state-" + hex.EncodeToString(id) + ".tmp"
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return root.Rename(name, "state.json")
}

// Status returns the persisted state. Ports are listed (not listening) when the
// honeypot is off so callers can show what would be opened.
func (h Honeypot) Status() (HoneypotState, error) {
	var out HoneypotState
	err := h.locked(func(_ *os.Root, s *HoneypotState) error { out = *s; return nil })
	if err == nil && len(out.Ports) == 0 {
		for _, svc := range h.serviceList() {
			out.Ports = append(out.Ports, HoneypotPort{Port: svc.Port, Service: svc.Name})
		}
	}
	return out, err
}

// Configure turns the honeypot on or off. The running agent opens or closes
// the listeners within one tick. Alerts and history are kept when disabling.
func (h Honeypot) Configure(enabled bool) error {
	return h.locked(func(root *os.Root, s *HoneypotState) error {
		if s.Enabled == enabled {
			return nil
		}
		s.Enabled = enabled
		s.Ports = nil
		s.Status = "disabled"
		if enabled {
			s.Status = "starting"
		}
		return saveHoneypot(root, s)
	})
}

type honeypotHit struct {
	at      time.Time
	service string
	port    int
	source  netip.Addr
}

// SourceClass groups a remote address: "mesh" is another computer in the
// neXal network (100.64.0.0/10), "lan" is the local network, "other" is
// anything else. Loopback returns "".
func SourceClass(addr netip.Addr) string {
	addr = addr.Unmap()
	switch {
	case !addr.IsValid() || addr.IsLoopback() || addr.IsUnspecified():
		return ""
	case addr.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(addr):
		return "mesh"
	case addr.IsPrivate() || addr.IsLinkLocalUnicast():
		return "lan"
	default:
		return "other"
	}
}

type honeypotLimiter struct {
	last   map[string]time.Time
	window time.Time
	count  int
}

func (l *honeypotLimiter) allow(hit honeypotHit) bool {
	if l.last == nil {
		l.last = map[string]time.Time{}
	}
	if hit.at.Sub(l.window) >= time.Hour {
		l.window, l.count = hit.at, 0
	}
	key := fmt.Sprintf("%s|%d", hit.source, hit.port)
	if prev, ok := l.last[key]; ok && hit.at.Sub(prev) < honeypotSourceCooloff {
		return false
	}
	if l.count >= honeypotHourlyEvents {
		return false
	}
	if len(l.last) > 1024 {
		for k, t := range l.last {
			if hit.at.Sub(t) >= honeypotSourceCooloff {
				delete(l.last, k)
			}
		}
	}
	l.last[key] = hit.at
	l.count++
	return true
}

func honeypotEvent(hit honeypotHit) (Event, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Event{}, err
	}
	// The tag groups repeat visits from one source without sending the address.
	tag := sha256.Sum256([]byte(hit.source.String()))
	return Event{SchemaVersion: 1, EventID: "honeypot_" + hex.EncodeToString(id), ObservedAt: hit.at.UTC().Format(TimeLayout),
		Kind: "network_alert", Severity: "medium", Detector: "nexal_honeypot_" + hit.service, DetectorVersion: "1",
		OriginAssessment: "unknown", EvidenceRef: "honeypot_" + hit.service + "_" + SourceClass(hit.source) + "_" + hex.EncodeToString(tag[:6])}, nil
}

func recordHoneypotHit(s *HoneypotState, hit honeypotHit, limiter *honeypotLimiter) {
	s.Triggers = min(s.Triggers+1, honeypotCountCap)
	s.LastTriggeredAt = hit.at.UTC().Format(TimeLayout)
	s.Recent = append([]HoneypotConnection{{ObservedAt: s.LastTriggeredAt, Service: hit.service, Port: hit.port,
		SourceAddress: hit.source.Unmap().String(), SourceClass: SourceClass(hit.source)}}, s.Recent...)
	if len(s.Recent) > MaxHoneypotRecent {
		s.Recent = s.Recent[:MaxHoneypotRecent]
	}
	if !limiter.allow(hit) || len(s.Pending) >= MaxHoneypotPending {
		s.Suppressed = min(s.Suppressed+1, honeypotCountCap)
		return
	}
	if event, err := honeypotEvent(hit); err == nil {
		s.Pending = append(s.Pending, event)
	}
}

func handleHoneypotConn(conn net.Conn, svc HoneypotService) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(honeypotHold))
	if len(svc.Banner) > 0 {
		if _, err := conn.Write(svc.Banner); err != nil {
			return
		}
	}
	// Read and discard a little so the client sees a live service; nothing is kept.
	_, _ = io.Copy(io.Discard, io.LimitReader(conn, honeypotReadLimit))
}

// honeypotAddresses lists the local addresses the decoys may bind: this Mac's
// neXal network address and private-LAN addresses only. A public address is
// never bound, so the honeypot never opens a port to the internet.
func (h Honeypot) honeypotAddresses() []netip.Addr {
	var out []netip.Addr
	if h.allowLoopback {
		out = append(out, netip.MustParseAddr("127.0.0.1"))
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		prefix, err := netip.ParsePrefix(a.String())
		if err != nil {
			continue
		}
		addr := prefix.Addr().Unmap()
		// IPv4 only: link-local IPv6 needs zones and the mesh is IPv4.
		if addr.Is4() && (SourceClass(addr) == "mesh" || SourceClass(addr) == "lan") {
			out = append(out, addr)
		}
	}
	return out
}

// Run keeps the listeners in step with the enabled flag and this Mac's current
// addresses, records connections and delivers queued alerts. It returns when
// ctx is cancelled.
func (h Honeypot) Run(ctx context.Context, report func(context.Context, Event) error) {
	hits := make(chan honeypotHit, 64)
	sem := make(chan struct{}, honeypotConcurrency)
	listeners := map[string]net.Listener{} // service|address
	listenErr := map[string]string{}       // service -> last bind error
	boundPort := map[string]int{}          // service -> actual port
	var wg sync.WaitGroup
	closeKey := func(key string) {
		if l, ok := listeners[key]; ok {
			l.Close()
			delete(listeners, key)
		}
	}
	closeAll := func() {
		for key := range listeners {
			closeKey(key)
		}
		clear(listenErr)
	}
	defer func() { closeAll(); wg.Wait() }()
	var limiter honeypotLimiter
	var queued []honeypotHit
	ticker := time.NewTicker(honeypotTick)
	defer ticker.Stop()
	for {
		state, err := h.Status()
		enabled := err == nil && state.Enabled
		if !enabled {
			closeAll()
		} else {
			wanted := map[string]bool{}
			addresses := h.honeypotAddresses()
			for _, svc := range h.serviceList() {
				delete(listenErr, svc.Name)
				if len(addresses) == 0 {
					listenErr[svc.Name] = "no_private_address"
				}
				for _, addr := range addresses {
					key := svc.Name + "|" + addr.String()
					wanted[key] = true
					if _, open := listeners[key]; open {
						continue
					}
					port := svc.Port
					if p, ok := boundPort[svc.Name]; ok && svc.Port == 0 {
						port = p // tests: keep one ephemeral port across addresses
					}
					l, err := net.Listen("tcp4", netip.AddrPortFrom(addr, uint16(port)).String())
					if err != nil {
						code := "failed"
						if errors.Is(err, syscall.EADDRINUSE) {
							code = "in_use"
						} else if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
							code = "denied"
						}
						listenErr[svc.Name] = code
						continue
					}
					if tcp, ok := l.Addr().(*net.TCPAddr); ok {
						boundPort[svc.Name] = tcp.Port
					}
					listeners[key] = l
					wg.Add(1)
					go func(l net.Listener, svc HoneypotService, port int) {
						defer wg.Done()
						for {
							conn, err := l.Accept()
							if err != nil {
								if errors.Is(err, net.ErrClosed) {
									return
								}
								time.Sleep(100 * time.Millisecond)
								continue
							}
							remote, _ := netip.ParseAddrPort(conn.RemoteAddr().String())
							if SourceClass(remote.Addr()) == "" && !h.allowLoopback {
								conn.Close()
								continue
							}
							select {
							case hits <- honeypotHit{at: time.Now(), service: svc.Name, port: port, source: remote.Addr()}:
							default: // alert channel full: the connection is still handled
							}
							select {
							case sem <- struct{}{}:
								go func() { defer func() { <-sem }(); handleHoneypotConn(conn, svc) }()
							default:
								conn.Close()
							}
						}
					}(l, svc, boundPort[svc.Name])
				}
			}
			// Addresses that went away (Wi-Fi change, mesh down) are closed.
			for key := range listeners {
				if !wanted[key] {
					closeKey(key)
				}
			}
		}
		// Persist listener status and connections; keep them in memory if the
		// state is momentarily busy.
		_ = h.locked(func(root *os.Root, s *HoneypotState) error {
			if s.Enabled {
				s.Ports = nil
				open := 0
				for _, svc := range h.serviceList() {
					port := svc.Port
					if p, ok := boundPort[svc.Name]; ok {
						port = p
					}
					p := HoneypotPort{Port: port, Service: svc.Name, Error: listenErr[svc.Name]}
					for key := range listeners {
						if strings.HasPrefix(key, svc.Name+"|") {
							p.Listening = true
						}
					}
					if p.Listening {
						open++
						p.Error = ""
					}
					s.Ports = append(s.Ports, p)
				}
				switch {
				case open == len(h.serviceList()):
					s.Status = "listening"
				case open > 0:
					s.Status = "degraded"
				default:
					s.Status = "error"
				}
			}
			for _, hit := range queued {
				recordHoneypotHit(s, hit, &limiter)
			}
			queued = nil
			return saveHoneypot(root, s)
		})
		if report != nil {
			h.deliver(ctx, report)
		}
		select {
		case <-ctx.Done():
			return
		case hit := <-hits:
			queued = append(queued, hit)
			for len(queued) < 64 {
				select {
				case hit = <-hits:
					queued = append(queued, hit)
					continue
				default:
				}
				break
			}
		case <-ticker.C:
		}
	}
}

// deliver sends queued alerts oldest first, removing each only after the
// coordinator accepted it. Retries resend the identical event.
func (h Honeypot) deliver(ctx context.Context, report func(context.Context, Event) error) {
	state, err := h.Status()
	if err != nil {
		return
	}
	now := time.Now()
	for _, event := range state.Pending {
		if ctx.Err() != nil {
			return
		}
		expired := event.Validate(now) != nil
		if !expired {
			request, cancel := context.WithTimeout(ctx, 15*time.Second)
			err = report(request, event)
			cancel()
			if err != nil {
				return
			}
		}
		_ = h.locked(func(root *os.Root, s *HoneypotState) error {
			for i, pending := range s.Pending {
				if pending.EventID == event.EventID {
					s.Pending = append(s.Pending[:i], s.Pending[i+1:]...)
					if expired {
						s.Suppressed = min(s.Suppressed+1, honeypotCountCap)
					}
					return saveHoneypot(root, s)
				}
			}
			return nil
		})
	}
}
