package sandbox

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// MaxQCOW2VirtualBytes bounds the virtual size a conversion will produce.
const MaxQCOW2VirtualBytes = 64 << 30

const (
	qcowMagic          = 0x514649fb
	qcowMinClusterBits = 9  // 512 B
	qcowMaxClusterBits = 21 // 2 MiB
	qcowMaxL1Entries   = 1 << 22

	qcowOffsetMask = 0x00fffffffffffe00

	qcowIncompatDirty       = 1 << 0 // harmless for reading
	qcowIncompatCorrupt     = 1 << 1
	qcowIncompatExternal    = 1 << 2
	qcowIncompatCompression = 1 << 3
	qcowIncompatExtendedL2  = 1 << 4

	qcowExtEnd          = 0
	qcowExtExternalData = 0x44415441
)

// QCOW2Converter is a pure-Go qcow2 to raw converter (no qemu-img). It handles
// qcow2 v2 and v3 with 512 B to 2 MiB clusters, zero/unallocated clusters,
// standard clusters and DEFLATE-compressed clusters. It refuses encrypted
// images, external data files, backing files, zstd compression, extended L2
// entries and virtual sizes above MaxQCOW2VirtualBytes. Output is sparse and
// memory use is a few cluster-sized buffers plus the L1 table.
type QCOW2Converter struct{}

var _ Converter = QCOW2Converter{}

type qcowHeader struct {
	version     uint32
	clusterBits uint32
	clusterSize int64
	size        int64
	l1Size      uint32
	l1Offset    int64
}

var errQCOWCorrupt = errors.New("qcow2: corrupt image")

func qcowCorrupt(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errQCOWCorrupt, fmt.Sprintf(format, a...))
}

// ToRaw converts the qcow2 at src to a sparse raw image at dst.
func (QCOW2Converter) ToRaw(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	h, err := readQCOWHeader(in, st.Size())
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := convertQCOW(ctx, in, st.Size(), h, out); err != nil {
		out.Close()
		return err
	}
	if err := out.Truncate(h.size); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func readQCOWHeader(r io.ReaderAt, fileSize int64) (*qcowHeader, error) {
	var b [112]byte
	n, err := r.ReadAt(b[:], 0)
	if err != nil && !(errors.Is(err, io.EOF) && n >= 72) {
		return nil, qcowCorrupt("header truncated")
	}
	be := binary.BigEndian
	if be.Uint32(b[0:]) != qcowMagic {
		return nil, errors.New("qcow2: bad magic")
	}
	h := &qcowHeader{version: be.Uint32(b[4:])}
	if h.version != 2 && h.version != 3 {
		return nil, fmt.Errorf("qcow2: unsupported version %d", h.version)
	}
	if be.Uint64(b[8:]) != 0 || be.Uint32(b[16:]) != 0 {
		return nil, errors.New("qcow2: backing files are not supported")
	}
	h.clusterBits = be.Uint32(b[20:])
	if h.clusterBits < qcowMinClusterBits || h.clusterBits > qcowMaxClusterBits {
		return nil, qcowCorrupt("cluster size 2^%d out of range", h.clusterBits)
	}
	h.clusterSize = int64(1) << h.clusterBits
	sz := be.Uint64(b[24:])
	if sz == 0 {
		return nil, qcowCorrupt("zero virtual size")
	}
	if sz > MaxQCOW2VirtualBytes {
		return nil, fmt.Errorf("qcow2: virtual size %d exceeds the %d GiB limit", sz, MaxQCOW2VirtualBytes>>30)
	}
	h.size = int64(sz)
	if be.Uint32(b[32:]) != 0 {
		return nil, errors.New("qcow2: encrypted images are not supported")
	}
	h.l1Size = be.Uint32(b[36:])
	h.l1Offset = int64(be.Uint64(b[40:]) & qcowOffsetMask)
	hdrLen := int64(72)
	if h.version == 3 {
		if n < 104 {
			return nil, qcowCorrupt("v3 header truncated")
		}
		incompat := be.Uint64(b[72:])
		hdrLen = int64(be.Uint32(b[100:]))
		if hdrLen < 104 || hdrLen%8 != 0 || hdrLen > h.clusterSize {
			return nil, qcowCorrupt("bad header length %d", hdrLen)
		}
		if incompat&qcowIncompatCorrupt != 0 {
			return nil, errors.New("qcow2: image is marked corrupt")
		}
		if incompat&qcowIncompatExternal != 0 {
			return nil, errors.New("qcow2: external data files are not supported")
		}
		if incompat&qcowIncompatExtendedL2 != 0 {
			return nil, errors.New("qcow2: extended L2 entries are not supported")
		}
		if incompat&qcowIncompatCompression != 0 {
			if hdrLen < 105 || n < 105 || b[104] != 0 {
				return nil, errors.New("qcow2: zstd-compressed clusters are not supported (only deflate)")
			}
		}
		if rest := incompat &^ (qcowIncompatDirty | qcowIncompatCorrupt | qcowIncompatExternal |
			qcowIncompatCompression | qcowIncompatExtendedL2); rest != 0 {
			return nil, fmt.Errorf("qcow2: unknown incompatible feature bits %#x", rest)
		}
	}
	if err := scanQCOWExtensions(r, hdrLen, h.clusterSize, fileSize); err != nil {
		return nil, err
	}
	l2Entries := h.clusterSize / 8
	need := (h.size + h.clusterSize*l2Entries - 1) / (h.clusterSize * l2Entries)
	if int64(h.l1Size) < need || h.l1Size > qcowMaxL1Entries {
		return nil, qcowCorrupt("L1 table has %d entries, need %d", h.l1Size, need)
	}
	if h.l1Offset < h.clusterSize || h.l1Offset%h.clusterSize != 0 ||
		h.l1Offset+int64(h.l1Size)*8 > fileSize {
		return nil, qcowCorrupt("L1 table offset out of range")
	}
	return h, nil
}

// scanQCOWExtensions walks the header extensions only to refuse an external
// data file name; everything else is skipped.
func scanQCOWExtensions(r io.ReaderAt, off, clusterSize, fileSize int64) error {
	for i := 0; i < 64; i++ {
		var e [8]byte
		if off+8 > clusterSize || off+8 > fileSize {
			return nil
		}
		if _, err := r.ReadAt(e[:], off); err != nil {
			return nil
		}
		typ, ln := binary.BigEndian.Uint32(e[0:]), int64(binary.BigEndian.Uint32(e[4:]))
		switch typ {
		case qcowExtEnd:
			return nil
		case qcowExtExternalData:
			return errors.New("qcow2: external data files are not supported")
		}
		off += 8 + (ln+7)&^7
	}
	return nil
}

func convertQCOW(ctx context.Context, in io.ReaderAt, fileSize int64, h *qcowHeader, out io.WriterAt) error {
	be := binary.BigEndian
	cs := h.clusterSize
	l1 := make([]byte, int64(h.l1Size)*8)
	if _, err := in.ReadAt(l1, h.l1Offset); err != nil {
		return qcowCorrupt("reading L1 table: %v", err)
	}
	l2 := make([]byte, cs)
	data := make([]byte, cs)
	comp := make([]byte, 2*cs+512)
	l2Entries := cs / 8
	var fr io.ReadCloser
	var zero = make([]byte, cs)

	for i := int64(0); i < int64(h.l1Size); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := be.Uint64(l1[i*8:])
		l2Off := int64(e & qcowOffsetMask)
		if l2Off == 0 {
			continue
		}
		base := i * l2Entries * cs
		if base >= h.size {
			continue
		}
		if l2Off < cs || l2Off%cs != 0 || l2Off+cs > fileSize {
			return qcowCorrupt("L2 table offset %d out of range", l2Off)
		}
		if _, err := in.ReadAt(l2, l2Off); err != nil {
			return qcowCorrupt("reading L2 table: %v", err)
		}
		for j := int64(0); j < l2Entries; j++ {
			guest := base + j*cs
			if guest >= h.size {
				break
			}
			n := cs
			if guest+n > h.size {
				n = h.size - guest
			}
			ent := be.Uint64(l2[j*8:])
			var buf []byte
			if ent&(1<<62) != 0 { // compressed cluster
				x := uint(62 - (h.clusterBits - 8))
				hostOff := int64(ent & (1<<x - 1))
				sectors := int64(ent >> x & (1<<(62-x) - 1))
				csize := (sectors+1)*512 - hostOff&511
				if hostOff < cs || csize <= 0 || csize > int64(len(comp)) || hostOff+csize > fileSize {
					return qcowCorrupt("compressed cluster at %d (%d bytes) out of range", hostOff, csize)
				}
				if _, err := in.ReadAt(comp[:csize], hostOff); err != nil {
					return qcowCorrupt("reading compressed cluster: %v", err)
				}
				src := bytes.NewReader(comp[:csize])
				if fr == nil {
					fr = flate.NewReader(src)
				} else if err := fr.(flate.Resetter).Reset(src, nil); err != nil {
					return err
				}
				if _, err := io.ReadFull(fr, data[:n]); err != nil {
					return qcowCorrupt("inflating cluster at %d: %v", hostOff, err)
				}
				buf = data[:n]
			} else {
				if h.version == 3 && ent&1 != 0 { // zero cluster
					continue
				}
				hostOff := int64(ent & qcowOffsetMask)
				if hostOff == 0 { // unallocated
					continue
				}
				if hostOff < cs || hostOff%cs != 0 || hostOff+cs > fileSize {
					return qcowCorrupt("data cluster offset %d out of range", hostOff)
				}
				if _, err := in.ReadAt(data[:n], hostOff); err != nil {
					return qcowCorrupt("reading data cluster: %v", err)
				}
				buf = data[:n]
			}
			if bytes.Equal(buf, zero[:len(buf)]) {
				continue // keep the output sparse
			}
			if _, err := out.WriteAt(buf, guest); err != nil {
				return err
			}
		}
	}
	return nil
}
