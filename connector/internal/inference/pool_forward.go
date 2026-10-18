package inference

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// ForwardAPI answers on listen (the Mac's neXal address) and passes every request on to the pool server
// listening on target (loopback). A Mac cannot open a connection to its own neXal address, so the server
// itself listens on loopback, where the Mac can check that it answers, and only this forwarder is
// reachable from the other Macs. It runs until ctx ends.
func ForwardAPI(ctx context.Context, listen, target string) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	u := &url.URL{Scheme: "http", Host: target}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.FlushInterval = -1 // stream tokens as they arrive
	srv := &http.Server{Handler: rp}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() { _ = srv.Serve(ln) }()
	return nil
}
