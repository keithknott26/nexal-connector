package discovery

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"strings"
)

// The DNS message codec is hand-rolled on purpose. The connector has zero
// third-party dependencies and the convenient mDNS libraries pull in miekg/dns
// plus several golang.org/x modules; DNS-SD is a bounded, reviewable amount of
// encoding, so it is written here instead of acquired.
//
// Every decode bound below exists because multicast is hostile input: any
// machine on a café, hotel or office network can send these bytes, unsolicited
// and unauthenticated. A malformed packet must cost a bounded amount of memory
// and time and must never panic. Nothing this file returns is trusted; see
// peers.go for why a decoded record can never widen an allowlist.
const (
	// maxMessageBytes bounds a single datagram. RFC 6762 permits large
	// multicast messages, but a neXal announcement needs well under 1 KB, so
	// anything past this is either not ours or is trying to make us allocate.
	maxMessageBytes = 9000
	// maxName is the DNS wire limit for a fully qualified name.
	maxName = 255
	// maxLabel is the DNS wire limit for one label.
	maxLabel = 63
	// maxQuestions and maxSectionRecords bound per-section counts; a header can
	// claim 65535 records, and the counts are attacker-chosen.
	maxQuestions      = 8
	maxSectionRecords = 64
	maxTotalRecords   = 128
	// TXT bounds. A neXal TXT record carries three short keys; anything larger
	// is not ours.
	maxTXTStringLen = 255
	maxTXTStrings   = 16
	maxTXTBytes     = 1024
)

var (
	// ErrMalformed is returned for every rejected decode. The specific reason is
	// deliberately not exposed to callers: it would only encourage a caller to
	// treat "nearly valid" as valid, and error text derived from network bytes
	// is its own small hazard.
	ErrMalformed = errors.New("discovery: malformed DNS message")
	// ErrTooLarge is returned when an encode would exceed the datagram bound.
	ErrTooLarge = errors.New("discovery: DNS message too large")
)

// DNS record and class values used by DNS-SD.
const (
	typeA     uint16 = 1
	typePTR   uint16 = 12
	typeTXT   uint16 = 16
	typeAAAA  uint16 = 28
	typeSRV   uint16 = 33
	typeANY   uint16 = 255
	classIN   uint16 = 1
	classMask uint16 = 0x7fff // Strips the mDNS unicast-response/cache-flush bit.

	flagResponse uint16 = 0x8000
	flagTruncate uint16 = 0x0200
	flagOpcode   uint16 = 0x7800
	flagRcode    uint16 = 0x000f
)

type question struct {
	name  string
	qtype uint16
	class uint16
}

// record holds only the record types DNS-SD needs. Unknown types are skipped
// during decode rather than retained: keeping opaque attacker-supplied rdata
// buys nothing and invites somebody to parse it later.
type record struct {
	name  string
	rtype uint16
	class uint16
	ttl   uint32

	ptr    string   // typePTR
	target string   // typeSRV
	port   uint16   // typeSRV
	txt    []string // typeTXT
	addr   netip.Addr
}

type message struct {
	id    uint16
	flags uint16
	// questions, answers and extra (authority plus additional, merged: DNS-SD
	// puts SRV/TXT/A wherever it likes and the distinction grants nothing).
	questions []question
	answers   []record
	extra     []record
}

func (m *message) response() bool  { return m.flags&flagResponse != 0 }
func (m *message) truncated() bool { return m.flags&flagTruncate != 0 }

// decodeMessage parses a DNS message with hard bounds on every length the
// message itself supplies. It allocates at most O(len(buf)).
func decodeMessage(buf []byte) (*message, error) {
	if len(buf) < 12 || len(buf) > maxMessageBytes {
		return nil, ErrMalformed
	}
	m := &message{id: binary.BigEndian.Uint16(buf[0:2]), flags: binary.BigEndian.Uint16(buf[2:4])}
	qd := int(binary.BigEndian.Uint16(buf[4:6]))
	an := int(binary.BigEndian.Uint16(buf[6:8]))
	ns := int(binary.BigEndian.Uint16(buf[8:10]))
	ar := int(binary.BigEndian.Uint16(buf[10:12]))
	if qd > maxQuestions || an > maxSectionRecords || ns > maxSectionRecords ||
		ar > maxSectionRecords || an+ns+ar > maxTotalRecords {
		return nil, ErrMalformed
	}
	// Counts are attacker-chosen, so capacity is derived from the declared count
	// only after the count has been bounded above.
	off := 12
	m.questions = make([]question, 0, qd)
	for range qd {
		name, next, err := decodeName(buf, off)
		if err != nil {
			return nil, err
		}
		off = next
		if off+4 > len(buf) {
			return nil, ErrMalformed
		}
		m.questions = append(m.questions, question{
			name:  name,
			qtype: binary.BigEndian.Uint16(buf[off : off+2]),
			class: binary.BigEndian.Uint16(buf[off+2:off+4]) & classMask,
		})
		off += 4
	}
	m.answers = make([]record, 0, an)
	m.extra = make([]record, 0, ns+ar)
	for i := range an + ns + ar {
		rr, next, err := decodeRecord(buf, off)
		if err != nil {
			return nil, err
		}
		off = next
		if i < an {
			m.answers = append(m.answers, rr)
		} else {
			m.extra = append(m.extra, rr)
		}
	}
	// Trailing bytes are not an error in DNS practice, and refusing them would
	// reject padded datagrams from other stacks. They are simply not read.
	return m, nil
}

func decodeRecord(buf []byte, off int) (record, int, error) {
	name, next, err := decodeName(buf, off)
	if err != nil {
		return record{}, 0, err
	}
	off = next
	if off+10 > len(buf) {
		return record{}, 0, ErrMalformed
	}
	rr := record{
		name:  name,
		rtype: binary.BigEndian.Uint16(buf[off : off+2]),
		class: binary.BigEndian.Uint16(buf[off+2:off+4]) & classMask,
		ttl:   binary.BigEndian.Uint32(buf[off+4 : off+8]),
	}
	length := int(binary.BigEndian.Uint16(buf[off+8 : off+10]))
	off += 10
	if length > len(buf)-off {
		return record{}, 0, ErrMalformed
	}
	end := off + length
	switch rr.rtype {
	case typePTR:
		// A PTR target may itself be compressed, so it is decoded against the
		// whole message; the same strictly-decreasing pointer rule applies.
		ptr, consumed, err := decodeName(buf, off)
		if err != nil || consumed > end {
			return record{}, 0, ErrMalformed
		}
		rr.ptr = ptr
	case typeSRV:
		if length < 7 {
			return record{}, 0, ErrMalformed
		}
		rr.port = binary.BigEndian.Uint16(buf[off+4 : off+6])
		target, consumed, err := decodeName(buf, off+6)
		if err != nil || consumed > end {
			return record{}, 0, ErrMalformed
		}
		rr.target = target
	case typeTXT:
		txt, err := decodeTXT(buf[off:end])
		if err != nil {
			return record{}, 0, err
		}
		rr.txt = txt
	case typeA:
		if length != 4 {
			return record{}, 0, ErrMalformed
		}
		rr.addr = netip.AddrFrom4([4]byte(buf[off : off+4]))
	case typeAAAA:
		if length != 16 {
			return record{}, 0, ErrMalformed
		}
		rr.addr = netip.AddrFrom16([16]byte(buf[off : off+16]))
	default:
		// Unknown type: skip the rdata without retaining it.
	}
	return rr, end, nil
}

func decodeTXT(rdata []byte) ([]string, error) {
	if len(rdata) > maxTXTBytes {
		return nil, ErrMalformed
	}
	var out []string
	for i := 0; i < len(rdata); {
		n := int(rdata[i])
		if n > maxTXTStringLen || i+1+n > len(rdata) {
			return nil, ErrMalformed
		}
		if len(out) >= maxTXTStrings {
			return nil, ErrMalformed
		}
		out = append(out, string(rdata[i+1:i+1+n]))
		i += 1 + n
	}
	return out, nil
}

// decodeName reads a possibly compressed name and returns the name plus the
// offset immediately after the name *as encoded at off* (not after any pointer
// target it followed).
//
// The termination argument is the whole point of this function. Every
// compression pointer must target a strictly smaller offset than every pointer
// followed before it. That makes the sequence of pointer targets strictly
// decreasing and bounded below by zero, so the loop cannot run more than
// len(buf) times and a packet whose pointer targets itself — the classic
// 0xc0 0x0c self-reference — is rejected instead of hanging the parser. The
// accumulated name is separately bounded by maxName, so a legal-looking chain
// of many short labels cannot grow memory without bound either.
func decodeName(buf []byte, off int) (string, int, error) {
	if off < 0 || off >= len(buf) {
		return "", 0, ErrMalformed
	}
	var name strings.Builder
	// ceiling is the exclusive upper bound for the next pointer target. It
	// starts at len(buf) and only ever decreases.
	ceiling := len(buf)
	consumed := -1
	cur := off
	for {
		if cur >= len(buf) {
			return "", 0, ErrMalformed
		}
		b := buf[cur]
		switch b & 0xc0 {
		case 0x00:
			n := int(b)
			if n == 0 {
				if consumed < 0 {
					consumed = cur + 1
				}
				if name.Len() == 0 {
					name.WriteByte('.') // The root name.
				}
				return name.String(), consumed, nil
			}
			if n > maxLabel || cur+1+n > len(buf) {
				return "", 0, ErrMalformed
			}
			// +1 for the dot this label contributes.
			if name.Len()+n+1 > maxName {
				return "", 0, ErrMalformed
			}
			label := buf[cur+1 : cur+1+n]
			for _, c := range label {
				// Printable ASCII only, and never a dot: neXal's own names are
				// ASCII, and refusing the rest keeps a decoded name from being
				// ambiguous when it is later compared or logged.
				if c < 0x21 || c > 0x7e || c == '.' {
					return "", 0, ErrMalformed
				}
			}
			name.Write(lowerASCII(label))
			name.WriteByte('.')
			cur += 1 + n
		case 0xc0:
			if cur+2 > len(buf) {
				return "", 0, ErrMalformed
			}
			target := int(binary.BigEndian.Uint16(buf[cur:cur+2]) & 0x3fff)
			if consumed < 0 {
				consumed = cur + 2
			}
			// Strictly decreasing, and strictly behind the pointer itself.
			if target >= ceiling || target >= cur {
				return "", 0, ErrMalformed
			}
			ceiling = target
			cur = target
		default:
			// 0x40 and 0x80 label types are reserved; treat as malformed rather
			// than guessing at an extension nobody sends.
			return "", 0, ErrMalformed
		}
	}
}

func lowerASCII(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// encodeMessage writes a message without compression pointers. Refusing to emit
// pointers costs a few dozen bytes per announcement and removes the encoder half
// of the compression-loop hazard entirely.
func encodeMessage(m *message) ([]byte, error) {
	if len(m.questions) > maxQuestions ||
		len(m.answers) > maxSectionRecords || len(m.extra) > maxSectionRecords {
		return nil, ErrTooLarge
	}
	buf := make([]byte, 12, 512)
	binary.BigEndian.PutUint16(buf[0:2], m.id)
	binary.BigEndian.PutUint16(buf[2:4], m.flags)
	binary.BigEndian.PutUint16(buf[4:6], uint16(len(m.questions)))
	binary.BigEndian.PutUint16(buf[6:8], uint16(len(m.answers)))
	binary.BigEndian.PutUint16(buf[8:10], 0)
	binary.BigEndian.PutUint16(buf[10:12], uint16(len(m.extra)))
	var err error
	for _, q := range m.questions {
		if buf, err = encodeName(buf, q.name); err != nil {
			return nil, err
		}
		buf = binary.BigEndian.AppendUint16(buf, q.qtype)
		buf = binary.BigEndian.AppendUint16(buf, q.class)
	}
	for _, rr := range append(append([]record{}, m.answers...), m.extra...) {
		if buf, err = encodeRecord(buf, rr); err != nil {
			return nil, err
		}
	}
	if len(buf) > maxMessageBytes {
		return nil, ErrTooLarge
	}
	return buf, nil
}

func encodeRecord(buf []byte, rr record) ([]byte, error) {
	var err error
	if buf, err = encodeName(buf, rr.name); err != nil {
		return nil, err
	}
	buf = binary.BigEndian.AppendUint16(buf, rr.rtype)
	buf = binary.BigEndian.AppendUint16(buf, rr.class)
	buf = binary.BigEndian.AppendUint32(buf, rr.ttl)
	var rdata []byte
	switch rr.rtype {
	case typePTR:
		if rdata, err = encodeName(nil, rr.ptr); err != nil {
			return nil, err
		}
	case typeSRV:
		rdata = make([]byte, 0, 32)
		rdata = binary.BigEndian.AppendUint16(rdata, 0) // priority
		rdata = binary.BigEndian.AppendUint16(rdata, 0) // weight
		rdata = binary.BigEndian.AppendUint16(rdata, rr.port)
		if rdata, err = encodeName(rdata, rr.target); err != nil {
			return nil, err
		}
	case typeTXT:
		if len(rr.txt) > maxTXTStrings {
			return nil, ErrTooLarge
		}
		for _, s := range rr.txt {
			if len(s) == 0 || len(s) > maxTXTStringLen {
				return nil, ErrTooLarge
			}
			rdata = append(rdata, byte(len(s)))
			rdata = append(rdata, s...)
		}
		if len(rdata) > maxTXTBytes {
			return nil, ErrTooLarge
		}
	case typeA:
		if !rr.addr.Is4() {
			return nil, ErrMalformed
		}
		a := rr.addr.As4()
		rdata = a[:]
	case typeAAAA:
		if !rr.addr.Is6() || rr.addr.Is4In6() {
			return nil, ErrMalformed
		}
		a := rr.addr.As16()
		rdata = a[:]
	default:
		return nil, ErrMalformed
	}
	if len(rdata) > 0xffff {
		return nil, ErrTooLarge
	}
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(rdata)))
	return append(buf, rdata...), nil
}

func encodeName(buf []byte, name string) ([]byte, error) {
	if name == "" || !strings.HasSuffix(name, ".") {
		return nil, ErrMalformed
	}
	total := 1
	for label := range strings.SplitSeq(strings.TrimSuffix(name, "."), ".") {
		if label == "" {
			if name == "." {
				break
			}
			return nil, ErrMalformed
		}
		if len(label) > maxLabel {
			return nil, ErrMalformed
		}
		for i := range len(label) {
			if label[i] < 0x21 || label[i] > 0x7e {
				return nil, ErrMalformed
			}
		}
		total += len(label) + 1
		if total > maxName {
			return nil, ErrMalformed
		}
		buf = append(buf, byte(len(label)))
		buf = append(buf, label...)
	}
	return append(buf, 0), nil
}
