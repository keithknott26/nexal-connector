package discovery

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// header builds a 12-byte DNS header with the given section counts.
func header(flags uint16, qd, an, ns, ar int) []byte {
	h := make([]byte, 12)
	binary.BigEndian.PutUint16(h[2:4], flags)
	binary.BigEndian.PutUint16(h[4:6], uint16(qd))
	binary.BigEndian.PutUint16(h[6:8], uint16(an))
	binary.BigEndian.PutUint16(h[8:10], uint16(ns))
	binary.BigEndian.PutUint16(h[10:12], uint16(ar))
	return h
}

func name(labels ...string) []byte {
	var out []byte
	for _, l := range labels {
		out = append(out, byte(len(l)))
		out = append(out, l...)
	}
	return append(out, 0)
}

// TestDecoderRejectsHostileMessages is the table the contract asks for: a
// malformed datagram from the local network must be refused, cheaply, without a
// panic and without an unbounded allocation.
func TestDecoderRejectsHostileMessages(t *testing.T) {
	selfReference := append(header(0, 1, 0, 0, 0), 0xc0, 0x0c)
	// A pointer at offset 12 targeting offset 16, which is itself a pointer back
	// to offset 12: the loop a parser without the decreasing rule follows forever.
	mutualReference := append(header(0, 1, 0, 0, 0), 0xc0, 0x10, 0x00, 0x00, 0xc0, 0x0c)
	forwardPointer := append(header(0, 1, 0, 0, 0), 0xc0, 0x20)
	longLabel := append(header(0, 1, 0, 0, 0), 0x40)
	longLabel = append(longLabel, strings.Repeat("a", 64)...)
	// Twenty 20-byte labels is over the 255-byte name limit.
	overlongName := header(0, 1, 0, 0, 0)
	for range 20 {
		overlongName = append(overlongName, 20)
		overlongName = append(overlongName, strings.Repeat("b", 20)...)
	}
	overlongName = append(overlongName, 0)
	dottedLabel := append(header(0, 1, 0, 0, 0), name("a.b", "local")...)
	reservedLabel := append(header(0, 1, 0, 0, 0), 0x80, 0x01)

	txtOverlong := append(header(flagResponse, 0, 1, 0, 0), name("x", "_nexal", "_tcp", "local")...)
	txtOverlong = append(txtOverlong, 0, 16, 0, 1, 0, 0, 0, 60)
	txtOverlong = binary.BigEndian.AppendUint16(txtOverlong, 8)
	txtOverlong = append(txtOverlong, 200) // Claims a 200-byte string in 7 bytes.
	txtOverlong = append(txtOverlong, []byte("abcdefg")...)

	badRDLength := append(header(flagResponse, 0, 1, 0, 0), name("host", "local")...)
	badRDLength = append(badRDLength, 0, 1, 0, 1, 0, 0, 0, 60, 0xff, 0xff)

	tests := []struct {
		name  string
		input []byte
	}{
		{"empty", nil},
		{"short header", header(0, 0, 0, 0, 0)[:11]},
		{"oversized message", make([]byte, maxMessageBytes+1)},
		{"question count beyond bound", header(0, maxQuestions+1, 0, 0, 0)},
		{"answer count beyond bound", header(flagResponse, 0, maxSectionRecords+1, 0, 0)},
		{"total record count beyond bound", header(flagResponse, 0, 60, 60, 60)},
		{"self-referential pointer", selfReference},
		{"mutually referential pointers", mutualReference},
		{"forward pointer", forwardPointer},
		{"pointer past end", append(header(0, 1, 0, 0, 0), 0xc3, 0xff)},
		{"truncated pointer", append(header(0, 1, 0, 0, 0), 0xc0)},
		{"label longer than 63", longLabel},
		{"label runs past end", append(header(0, 1, 0, 0, 0), 0x10, 'a')},
		{"name longer than 255", overlongName},
		{"dot inside label", dottedLabel},
		{"reserved label type", reservedLabel},
		{"unterminated name", append(header(0, 1, 0, 0, 0), 0x01, 'a')},
		{"question truncated after name", append(header(0, 1, 0, 0, 0), name("a")...)},
		{"txt string longer than rdata", txtOverlong},
		{"rdlength past end", badRDLength},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				defer close(done)
				if _, err := decodeMessage(tc.input); err == nil {
					t.Error("decoded a message that should have been rejected")
				}
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("decoder did not terminate on hostile input")
			}
		})
	}
}

// TestDecoderTruncationAtEveryOffset feeds every prefix of a well-formed message
// to the decoder. Truncation is the most common malformed packet in practice —
// a short read, a dropped fragment — and every prefix must either parse or be
// refused, never panic.
func TestDecoderTruncationAtEveryOffset(t *testing.T) {
	full := responsePacket(t, sampleAdvertisement())
	for i := range len(full) {
		if _, err := decodeMessage(full[:i]); err == nil && i < len(full) {
			// Parsing a strict prefix is acceptable only if the sections it
			// declares happen to be complete, which cannot happen here because
			// the counts in the header require all records.
			t.Fatalf("prefix of length %d decoded", i)
		}
	}
	if _, err := decodeMessage(full); err != nil {
		t.Fatalf("well-formed message rejected: %v", err)
	}
}

// TestDecoderAcceptsBackwardCompression proves the strictly-decreasing rule does
// not reject legal compression, which is what other stacks actually send.
func TestDecoderAcceptsBackwardCompression(t *testing.T) {
	buf := header(flagResponse, 0, 1, 0, 0)
	serviceAt := len(buf)
	buf = append(buf, name("_nexal", "_tcp", "local")...)
	buf = append(buf, 0, 12, 0, 1, 0, 0, 0, 60) // PTR IN, ttl 60
	// One literal label, then a pointer back to the service name at offset 12.
	rdata := []byte{4, 'm', 'a', 'c', '1', 0xc0, byte(serviceAt)}
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(rdata)))
	buf = append(buf, rdata...)
	m, err := decodeMessage(buf)
	if err != nil {
		t.Fatalf("legal backward compression rejected: %v", err)
	}
	if len(m.answers) != 1 || m.answers[0].ptr != "mac1."+ServiceName {
		t.Fatalf("compressed PTR decoded as %+v", m.answers)
	}
}

func TestNameCodecRoundTrip(t *testing.T) {
	for _, n := range []string{ServiceName, "mac1." + ServiceName, "mac1.local.", "."} {
		encoded, err := encodeName(nil, n)
		if err != nil {
			t.Fatalf("%q: %v", n, err)
		}
		decoded, consumed, err := decodeName(encoded, 0)
		if err != nil {
			t.Fatalf("%q: %v", n, err)
		}
		if consumed != len(encoded) {
			t.Fatalf("%q consumed %d of %d", n, consumed, len(encoded))
		}
		if decoded != n && !(n == "." && decoded == ".") {
			t.Fatalf("%q round-tripped as %q", n, decoded)
		}
	}
	for _, bad := range []string{"", "no-trailing-dot", "a..b.", strings.Repeat("x", 64) + ".", strings.Repeat("ab.", 100)} {
		if _, err := encodeName(nil, bad); err == nil {
			t.Fatalf("encoded invalid name %q", bad)
		}
	}
}

func TestEncoderRefusesOversizedRecords(t *testing.T) {
	if _, err := encodeRecord(nil, record{name: "x.local.", rtype: typeTXT, txt: []string{strings.Repeat("a", 256)}}); err == nil {
		t.Fatal("encoded an over-long TXT string")
	}
	if _, err := encodeRecord(nil, record{name: "x.local.", rtype: typeA, addr: netip.MustParseAddr("fd00::1")}); err == nil {
		t.Fatal("encoded an IPv6 address as an A record")
	}
	if _, err := encodeMessage(&message{answers: make([]record, maxSectionRecords+1)}); err == nil {
		t.Fatal("encoded a message with too many answers")
	}
}

// FuzzDecodeMessage exercises the whole hostile-input surface: the decoder, the
// candidate extractor and the responder all take raw multicast bytes.
func FuzzDecodeMessage(f *testing.F) {
	f.Add([]byte{})
	f.Add(append(header(0, 1, 0, 0, 0), 0xc0, 0x0c))                   // self-reference
	f.Add(append(header(0, 1, 0, 0, 0), 0x00, 0xc0, 0x0e, 0xc0, 0x0d)) // mutual reference
	f.Add(append(header(0, 1, 0, 0, 0), name("_nexal", "_tcp", "local")...))
	f.Add(responsePacket(f, sampleAdvertisement()))
	f.Add(queryPacket(f))
	responder, err := NewResponder(sampleAdvertisement(), func() time.Time { return time.Unix(0, 0) })
	if err != nil {
		f.Fatal(err)
	}
	from := netip.MustParseAddrPort("192.168.4.7:5353")
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxMessageBytes {
			return
		}
		m, err := decodeMessage(data)
		if err == nil {
			if len(m.answers)+len(m.extra) > maxTotalRecords || len(m.questions) > maxQuestions {
				t.Fatalf("decoded past the declared bounds: %d records", len(m.answers)+len(m.extra))
			}
			for _, rr := range append(m.answers, m.extra...) {
				if len(rr.name) > maxName || len(rr.txt) > maxTXTStrings {
					t.Fatalf("decoded record exceeds bounds: %+v", rr)
				}
			}
		}
		if reply, err := responder.Respond(data, from); err == nil && len(reply) > maxMessageBytes {
			t.Fatalf("reply of %d bytes", len(reply))
		}
		if candidates, err := ParseCandidates(data, from, time.Unix(1, 0)); err == nil {
			if len(candidates) > maxCandidatesPerPacket {
				t.Fatalf("%d candidates from one packet", len(candidates))
			}
			for _, c := range candidates {
				if !ValidFingerprint(c.Fingerprint) || !ValidHostID(c.HostID) {
					t.Fatalf("candidate escaped validation: %+v", c)
				}
			}
		}
	})
}
