package remoteinference

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"
)

// Forwarder answers on a Mac's neXal address and passes each request to whichever local server is
// current. The servers listen on loopback (a Mac cannot connect to its own neXal address, so it could not
// otherwise tell that they answer), and the pool swaps the server behind this one address as Macs join and
// leave without the address, or anyone using it, changing.
type Forwarder struct {
	ln    net.Listener
	srv   *http.Server
	addrs []string // every host:port this answers on, the main address first

	mu       sync.Mutex
	upstream string // host:port of the current server; "" while there is none
	notice   string // what to tell callers while there is none
	inflight int
	last     time.Time // the last request start or response byte
}

// NewForwarder starts listening on listen (ip:port), and on each alsoListen address. Loopback is added by
// the pool so this Mac can reach its own endpoint (it cannot connect to its own neXal address). Until
// SetUpstream is called every request gets a 503.
func NewForwarder(listen string, alsoListen ...string) (*Forwarder, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	lns := []net.Listener{ln}
	for _, a := range alsoListen {
		if a == listen {
			continue // the same address twice (a Mac whose neXal address is loopback in tests)
		}
		extra, err := net.Listen("tcp", a)
		if err != nil {
			for _, l := range lns {
				_ = l.Close()
			}
			return nil, err
		}
		lns = append(lns, extra)
	}
	f := &Forwarder{ln: ln, last: time.Now(), notice: "The model is starting."}
	for _, l := range lns {
		f.addrs = append(f.addrs, l.Addr().String())
	}
	f.srv = &http.Server{Handler: f}
	for _, l := range lns {
		go func(l net.Listener) { _ = f.srv.Serve(l) }(l)
	}
	return f, nil
}

// SetUpstream points the forwarder at a server on hostPort; "" stops forwarding and notice is what callers are told.
func (f *Forwarder) SetUpstream(hostPort, notice string) {
	f.mu.Lock()
	f.upstream, f.notice = hostPort, notice
	f.last = time.Now()
	f.mu.Unlock()
}

// Busy is the number of requests in flight and how long since any of them moved a byte.
func (f *Forwarder) Busy() (inflight int, quiet time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inflight, time.Since(f.last)
}

// Endpoints is every URL the forwarder answers on: the main (neXal) address, then LAN addresses, then
// loopback (which only works on this Mac).
func (f *Forwarder) Endpoints() []string {
	var main, lan, loop []string
	for i, a := range f.addrs {
		u := "http://" + a + "/v1"
		host, _, _ := net.SplitHostPort(a)
		switch ip := net.ParseIP(host); {
		case i == 0:
			main = append(main, u)
		case ip != nil && ip.IsLoopback():
			loop = append(loop, u)
		default:
			lan = append(lan, u)
		}
	}
	return append(append(main, lan...), loop...)
}

// Close stops listening and drops open connections.
func (f *Forwarder) Close() { _ = f.srv.Close() }

func (f *Forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	up, notice := f.upstream, f.notice
	f.mu.Unlock()
	if up == "" {
		fail(w, http.StatusServiceUnavailable, notice)
		return
	}
	f.mu.Lock()
	f.inflight++
	f.last = time.Now()
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inflight--; f.last = time.Now(); f.mu.Unlock() }()
	rp := &httputil.ReverseProxy{
		Director:      func(q *http.Request) { q.URL.Scheme, q.URL.Host = "http", up; q.Host = up },
		FlushInterval: -1, // stream tokens as they arrive
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			fail(w, http.StatusBadGateway, "The model stopped answering. If the pool lost a Mac it is restarting on the remaining Macs; try again in a minute.")
		},
	}
	rp.ServeHTTP(&activityWriter{ResponseWriter: w, f: f}, r)
}

// activityWriter notes every response byte so the pool can tell a long answer from a stuck one.
type activityWriter struct {
	http.ResponseWriter
	f *Forwarder
}

func (a *activityWriter) Write(p []byte) (int, error) {
	a.f.mu.Lock()
	a.f.last = time.Now()
	a.f.mu.Unlock()
	return a.ResponseWriter.Write(p)
}
func (a *activityWriter) Flush() {
	if fl, ok := a.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "pool_unavailable"}})
}
