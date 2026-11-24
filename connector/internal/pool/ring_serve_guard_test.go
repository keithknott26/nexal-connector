package pool

import (
	"errors"
	"net"
	"testing"
)

type fakeAddrListener struct {
	net.Listener
	addr net.Addr
}

func (l fakeAddrListener) Addr() net.Addr { return l.addr }

// serve must refuse a nil listener, a non-TCP address and a public or wildcard
// bind before taking ownership, and must refuse a second or post-close serve.
func TestRingServeRefusesUnsafeListenersAndReuse(t *testing.T) {
	tr := &ringTransport{}
	if err := tr.serve(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil listener = %v, want ErrInvalid", err)
	}
	for name, addr := range map[string]net.Addr{
		"unix socket": &net.UnixAddr{Name: "/tmp/x", Net: "unix"},
		"public":      &net.TCPAddr{IP: net.ParseIP("8.8.8.8"), Port: 1},
		"wildcard":    &net.TCPAddr{IP: net.IPv4zero, Port: 1},
	} {
		if err := tr.serve(fakeAddrListener{addr: addr}); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("%s = %v, want ErrUnsafePath", name, err)
		}
	}
	if tr.serving() {
		t.Fatal("a refused listener must not mark the transport as serving")
	}
	local := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}
	for name, state := range map[string]*ringTransport{
		"already served": {served: true},
		"closed":         {closed: true},
	} {
		if err := state.serve(fakeAddrListener{addr: local}); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s = %v, want ErrConflict", name, err)
		}
	}
}
