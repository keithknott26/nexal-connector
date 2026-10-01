package sandbox

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// qcCluster describes one guest cluster of a test image: 'd' data, 'z' zero
// cluster flag (v3), 'u' unallocated, 'c' deflate-compressed, 'Z' an allocated
// cluster whose data happens to be all zero.
type qcCluster struct {
	kind byte
	data []byte // for 'd' and 'c'; len == cluster size
}

type qcSpec struct {
	version  int
	bits     uint
	clusters []qcCluster
	trim     int64 // bytes removed from the virtual size (partial last cluster)
}

// buildQCOW is a minimal qcow2 writer: header cluster, L1 cluster(s), L2 tables,
// then data. Compressed clusters are packed unaligned.
func buildQCOW(sp qcSpec) (img []byte, raw []byte) {
	cs := 1 << sp.bits
	l2n := cs / 8
	n := len(sp.clusters)
	nl2 := (n + l2n - 1) / l2n
	l1Bytes := nl2 * 8
	l1Clusters := (l1Bytes + cs - 1) / cs
	img = make([]byte, cs*(1+l1Clusters))
	vsize := int64(n*cs) - sp.trim
	raw = make([]byte, vsize)

	l2 := make([][]byte, nl2)
	l2off := make([]int, nl2)
	// reserve L2 table clusters first for tables with any allocation
	for t := 0; t < nl2; t++ {
		used := false
		for j := 0; j < l2n && t*l2n+j < n; j++ {
			if sp.clusters[t*l2n+j].kind != 'u' {
				used = true
			}
		}
		if used {
			l2[t] = make([]byte, cs)
			l2off[t] = len(img)
			img = append(img, make([]byte, cs)...)
		}
	}
	be := binary.BigEndian
	for i, c := range sp.clusters {
		t, j := i/l2n, i%l2n
		var ent uint64
		switch c.kind {
		case 'u':
		case 'z':
			ent = 1
		case 'd', 'Z':
			if pad := len(img) % cs; pad != 0 {
				img = append(img, make([]byte, cs-pad)...)
			}
			ent = 1<<63 | uint64(len(img))
			img = append(img, c.data...)
			copy(raw[min(int64(i*cs), vsize):], c.data)
		case 'c':
			var b bytes.Buffer
			w, _ := flate.NewWriter(&b, flate.BestCompression)
			w.Write(c.data)
			w.Close()
			off := len(img)
			img = append(img, b.Bytes()...)
			x := uint(62 - (sp.bits - 8))
			sectors := uint64((off+b.Len()+511)/512 - off/512 - 1)
			ent = 1<<62 | sectors<<x | uint64(off)
			copy(raw[min(int64(i*cs), vsize):], c.data)
		}
		if l2[t] != nil {
			be.PutUint64(l2[t][j*8:], ent)
		}
	}
	for t := range l2 {
		if l2[t] != nil {
			copy(img[l2off[t]:], l2[t])
			be.PutUint64(img[cs+t*8:], 1<<63|uint64(l2off[t]))
		}
	}
	if pad := len(img) % cs; pad != 0 {
		img = append(img, make([]byte, cs-pad)...)
	}
	h := img[:cs]
	be.PutUint32(h[0:], 0x514649fb)
	be.PutUint32(h[4:], uint32(sp.version))
	be.PutUint32(h[20:], uint32(sp.bits))
	be.PutUint64(h[24:], uint64(vsize))
	be.PutUint32(h[36:], uint32(nl2))
	be.PutUint64(h[40:], uint64(cs))
	if sp.version == 3 {
		be.PutUint32(h[96:], 4)
		be.PutUint32(h[100:], 112)
	}
	return img, raw
}

func qcData(rng *rand.Rand, cs int) []byte {
	b := make([]byte, cs)
	rng.Read(b)
	return b
}

func qcPattern(cs int, v byte) []byte { return bytes.Repeat([]byte{v, v + 1, 0, 0}, cs/4+1)[:cs] }

func convertBytes(t *testing.T, img []byte) ([]byte, error) {
	t.Helper()
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a.qcow2"), filepath.Join(dir, "a.raw")
	if err := os.WriteFile(src, img, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (QCOW2Converter{}).ToRaw(context.Background(), src, dst); err != nil {
		return nil, err
	}
	out, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	return out, nil
}

func TestQCOW2Convert(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, version := range []int{2, 3} {
		for _, bits := range []uint{9, 12, 16, 21} {
			cs := 1 << bits
			kinds := "dzucdZuucdd"
			if version == 2 {
				kinds = strings.ReplaceAll(kinds, "z", "u")
			}
			var cl []qcCluster
			for _, k := range []byte(kinds) {
				c := qcCluster{kind: k}
				switch k {
				case 'd':
					c.data = qcData(rng, cs)
				case 'c':
					c.data = qcPattern(cs, byte(len(cl)))
				case 'Z':
					c.data = make([]byte, cs)
				}
				cl = append(cl, c)
			}
			for _, trim := range []int64{0, int64(cs / 2)} {
				img, want := buildQCOW(qcSpec{version: version, bits: bits, clusters: cl, trim: trim})
				got, err := convertBytes(t, img)
				if err != nil {
					t.Fatalf("v%d bits %d trim %d: %v", version, bits, trim, err)
				}
				if !bytes.Equal(got, want) {
					d := 0
					for d < len(got) && got[d] == want[d] {
						d++
					}
					t.Fatalf("v%d bits %d trim %d: output differs at %d/%d (cluster %d)", version, bits, trim, d, len(got), d/cs)
				}
			}
		}
	}
}

// Many clusters with an entirely unallocated L2 table (L1 entry 0) in the middle.
func TestQCOW2EmptyL2Table(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	var cl []qcCluster
	for i := 0; i < 64*3; i++ {
		c := qcCluster{kind: 'u'}
		if i < 64 || i >= 128 {
			c = qcCluster{kind: 'd', data: qcData(rng, 512)}
		}
		cl = append(cl, c)
	}
	img, want := buildQCOW(qcSpec{version: 3, bits: 9, clusters: cl})
	got, err := convertBytes(t, img)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("err=%v equal=%v", err, bytes.Equal(got, want))
	}
}

func TestQCOW2SparseOutput(t *testing.T) {
	cl := []qcCluster{{kind: 'u'}, {kind: 'u'}, {kind: 'd', data: bytes.Repeat([]byte{7}, 512)}}
	img, want := buildQCOW(qcSpec{version: 3, bits: 9, clusters: cl})
	got, err := convertBytes(t, img)
	if err != nil || len(got) != len(want) || !bytes.Equal(got, want) {
		t.Fatalf("err=%v len=%d", err, len(got))
	}
}

func TestQCOW2Refusals(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	base := func() []byte {
		img, _ := buildQCOW(qcSpec{version: 3, bits: 9, clusters: []qcCluster{
			{kind: 'd', data: qcData(rng, 512)}, {kind: 'c', data: qcPattern(512, 3)}}})
		return img
	}
	be := binary.BigEndian
	cases := map[string]func([]byte) []byte{
		"bad magic":         func(b []byte) []byte { b[0] = 'X'; return b },
		"bad version":       func(b []byte) []byte { be.PutUint32(b[4:], 9); return b },
		"truncated header":  func(b []byte) []byte { return b[:40] },
		"backing file":      func(b []byte) []byte { be.PutUint64(b[8:], 200); be.PutUint32(b[16:], 4); return b },
		"encrypted":         func(b []byte) []byte { be.PutUint32(b[32:], 1); return b },
		"cluster bits low":  func(b []byte) []byte { be.PutUint32(b[20:], 8); return b },
		"cluster bits high": func(b []byte) []byte { be.PutUint32(b[20:], 22); return b },
		"too large":         func(b []byte) []byte { be.PutUint64(b[24:], MaxQCOW2VirtualBytes+1); return b },
		"zero size":         func(b []byte) []byte { be.PutUint64(b[24:], 0); return b },
		"external data":     func(b []byte) []byte { be.PutUint64(b[72:], 1<<2); return b },
		"extended l2":       func(b []byte) []byte { be.PutUint64(b[72:], 1<<4); return b },
		"unknown incompat":  func(b []byte) []byte { be.PutUint64(b[72:], 1<<9); return b },
		"corrupt flag":      func(b []byte) []byte { be.PutUint64(b[72:], 1<<1); return b },
		"zstd": func(b []byte) []byte {
			be.PutUint64(b[72:], 1<<3)
			b[104] = 1
			return b
		},
		"l1 too small":     func(b []byte) []byte { be.PutUint32(b[36:], 0); return b },
		"l1 huge":          func(b []byte) []byte { be.PutUint32(b[36:], 0xffffffff); return b },
		"l1 out of file":   func(b []byte) []byte { be.PutUint64(b[40:], 1<<40); return b },
		"l1 over header":   func(b []byte) []byte { be.PutUint64(b[40:], 0); return b },
		"l2 out of file":   func(b []byte) []byte { be.PutUint64(b[512:], 1<<63|1<<40); return b },
		"data out of file": func(b []byte) []byte { be.PutUint64(b[1024:], 1<<63|1<<40); return b },
		"compressed over header": func(b []byte) []byte {
			be.PutUint64(b[1024+8:], 1<<62|100)
			return b
		},
		"compressed out of file": func(b []byte) []byte {
			be.PutUint64(b[1024+8:], 1<<62|1<<40)
			return b
		},
		"compressed garbage": func(b []byte) []byte {
			off := be.Uint64(b[1024+8:]) & (1<<54 - 1)
			for i := 0; i < 8; i++ {
				b[int(off)+i] = 0xff
			}
			return b
		},
		"truncated file": func(b []byte) []byte { return b[:len(b)-600] },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := convertBytes(t, mut(base())); err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
	// zstd error is explicit.
	b := base()
	be.PutUint64(b[72:], 1<<3)
	b[104] = 1
	if _, err := convertBytes(t, b); err == nil || !strings.Contains(err.Error(), "zstd") {
		t.Fatalf("want zstd error, got %v", err)
	}
}

func TestQCOW2DeflateCompressionTypeAccepted(t *testing.T) {
	img, want := buildQCOW(qcSpec{version: 3, bits: 9, clusters: []qcCluster{{kind: 'c', data: qcPattern(512, 1)}}})
	binary.BigEndian.PutUint64(img[72:], 1<<3) // compression_type bit set, type 0 = deflate
	got, err := convertBytes(t, img)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("err=%v", err)
	}
}

func TestQCOW2Cancel(t *testing.T) {
	img, _ := buildQCOW(qcSpec{version: 3, bits: 9, clusters: []qcCluster{{kind: 'd', data: qcPattern(512, 1)}}})
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	_ = os.WriteFile(src, img, 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (QCOW2Converter{}).ToRaw(ctx, src, filepath.Join(dir, "b")); err == nil {
		t.Fatal("expected context error")
	}
}

func FuzzQCOW2(f *testing.F) {
	img, _ := buildQCOW(qcSpec{version: 3, bits: 9, clusters: []qcCluster{
		{kind: 'd', data: qcPattern(512, 1)}, {kind: 'c', data: qcPattern(512, 2)}, {kind: 'z'}}})
	f.Add(img)
	f.Fuzz(func(t *testing.T, b []byte) {
		dir := t.TempDir()
		src := filepath.Join(dir, "a")
		if os.WriteFile(src, b, 0o600) != nil {
			return
		}
		_ = (QCOW2Converter{}).ToRaw(context.Background(), src, filepath.Join(dir, "b"))
	})
}
