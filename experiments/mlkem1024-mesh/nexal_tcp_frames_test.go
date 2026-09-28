package rosenpass

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func TestNexalTCPRejectsInvalidFrames(t *testing.T) {
	c := NewTCPConn("127.0.0.1:0")
	_, err := c.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.AuthorizeEndpoint(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}, true)
	endpoints, _ := c.LocalEndpoints()
	for _, tc := range []struct {
		name, magic string
		size, port  uint16
		body        []byte
	}{
		{"bad magic", "BAD!", 1, 9000, []byte{0}},
		{"oversized", "NXQ2", 4097, 9000, nil},
		{"empty", "NXQ2", 0, 9000, nil},
		{"unregistered port", "NXQ2", 1, 9001, []byte{0}},
		{"truncated", "NXQ2", 100, 9000, []byte{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", endpoints[0].String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			h := make([]byte, 8)
			copy(h, tc.magic)
			binary.BigEndian.PutUint16(h[4:6], tc.size)
			binary.BigEndian.PutUint16(h[6:8], tc.port)
			conn.Write(append(h, tc.body...))
			conn.(*net.TCPConn).CloseWrite()
			conn.SetReadDeadline(time.Now().Add(time.Second))
			var b [1]byte
			if _, err = conn.Read(b[:]); err == nil {
				t.Fatal("invalid frame left connection open")
			}
			select {
			case <-c.incoming:
				t.Fatal("invalid frame accepted")
			default:
			}
		})
	}
}
func TestNexalTCPReassemblesStreamAndChecksEnvelope(t *testing.T) {
	c := NewTCPConn("127.0.0.1:0")
	recv, err := c.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.AuthorizeEndpoint(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}, true)
	eps, _ := c.LocalEndpoints()
	conn, err := net.Dial("tcp", eps[0].String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Header and invalid envelope arrive in single-byte writes. Reassembly must
	// succeed, but the cryptographic envelope parser must reject the bogus bytes.
	frame := make([]byte, 72)
	copy(frame, "NXQ2")
	binary.BigEndian.PutUint16(frame[4:6], 64)
	binary.BigEndian.PutUint16(frame[6:8], 9000)
	for _, b := range frame {
		if _, err := conn.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var receipt [1]byte
	if _, err := conn.Read(receipt[:]); err != io.EOF {
		t.Fatalf("missing receipt: %v", err)
	}
	conn.Close()
	pub, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := recv[0](pub); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unauthenticated envelope accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("split TCP frame not reassembled")
	}
}

func TestNexalTCPSendWaitsForFrameConsumption(t *testing.T) {
	sender := NewTCPConn("127.0.0.1:0")
	if _, err := sender.Open(); err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	endpoint := &net.UDPAddr{IP: address.IP, Port: address.Port}
	sender.AuthorizeEndpoint(endpoint, true)
	pub, _, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	consumed := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(time.Second))
		var header [8]byte
		if _, err = io.ReadFull(conn, header[:]); err != nil {
			serverDone <- err
			return
		}
		body := make([]byte, int(binary.BigEndian.Uint16(header[4:6])))
		if _, err = io.ReadFull(conn, body); err != nil {
			serverDone <- err
			return
		}
		close(consumed)
		<-release
		serverDone <- nil
	}()
	sent := make(chan error, 1)
	go func() { sent <- sender.Send(&emptyData{}, pub, (*UDPEndpoint)(endpoint)) }()
	select {
	case <-consumed:
	case <-time.After(time.Second):
		t.Fatal("frame not received")
	}
	select {
	case err := <-sent:
		t.Fatalf("Send returned before receiver closed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release <- struct{}{}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Send did not complete after receiver consumed frame")
	}
}

// Receiving a final frame must not trigger key installation until its sender
// has observed delivery. Otherwise the response carrying delivery confirmation
// can be stranded under the old WireGuard session during re-keying.
func TestNexalTCPDispatchWaitsForSenderReceipt(t *testing.T) {
	c := NewTCPConn("127.0.0.1:0")
	if _, err := c.Open(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.AuthorizeEndpoint(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}, true)
	eps, _ := c.LocalEndpoints()
	conn, err := net.Dial("tcp", eps[0].String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	frame := make([]byte, 9)
	copy(frame, "NXQ2")
	binary.BigEndian.PutUint16(frame[4:6], 1)
	binary.BigEndian.PutUint16(frame[6:8], 9000)
	if _, err = conn.Write(frame); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.incoming:
		t.Fatal("frame dispatched before sender observed delivery")
	case <-time.After(100 * time.Millisecond):
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err = conn.Read(b[:]); err != io.EOF {
		t.Fatalf("missing delivery EOF: %v", err)
	}
	conn.Close()
	select {
	case <-c.incoming:
	case <-time.After(time.Second):
		t.Fatal("frame not dispatched after sender receipt")
	}
}
