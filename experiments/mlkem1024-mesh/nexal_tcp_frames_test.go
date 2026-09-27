package rosenpass

import (
	"encoding/binary"
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
