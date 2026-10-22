package sandbox

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestEffectiveAppProfile(t *testing.T) {
	withImage := func(ap string, desktop bool) Task {
		var tk Task
		tk.Image.AppProfile = ap
		tk.Desktop = desktop
		return tk
	}
	cases := []struct {
		name string
		task Task
		want string
	}{
		{"plain image, no desktop", withImage("", false), ""},
		{"none stays none without desktop", withImage(AppProfileNone, false), AppProfileNone},
		{"desktop on plain image gets the add-on", withImage("", true), AppProfileDesktop},
		{"desktop on none image gets the add-on", withImage(AppProfileNone, true), AppProfileDesktop},
		{"image profile wins over desktop", withImage(AppProfileKali, true), AppProfileKali},
		{"image profile without desktop", withImage(AppProfileJellyfin, false), AppProfileJellyfin},
	}
	for _, c := range cases {
		if got := effectiveAppProfile(c.task); got != c.want {
			t.Errorf("%s: effectiveAppProfile = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestScanQCOWExtensions(t *testing.T) {
	ext := func(typ uint32, payload []byte) []byte {
		b := make([]byte, 8, 8+len(payload)+8)
		binary.BigEndian.PutUint32(b[0:], typ)
		binary.BigEndian.PutUint32(b[4:], uint32(len(payload)))
		b = append(b, payload...)
		for len(b)%8 != 0 {
			b = append(b, 0)
		}
		return b
	}
	const cs = 4096
	scan := func(parts ...[]byte) error {
		buf := make([]byte, cs)
		copy(buf, bytes.Join(parts, nil))
		return scanQCOWExtensions(bytes.NewReader(buf), 0, cs, cs)
	}

	if err := scan(ext(qcowExtEnd, nil)); err != nil {
		t.Errorf("end-only: %v", err)
	}
	// An unknown extension with an unaligned length is skipped (padded to 8), then the end marker.
	if err := scan(ext(0xdeadbeef, []byte("abcde")), ext(qcowExtEnd, nil)); err != nil {
		t.Errorf("skipped unknown extension: %v", err)
	}
	// External data file after an unknown extension is refused.
	err := scan(ext(0xdeadbeef, []byte("abcde")), ext(qcowExtExternalData, []byte("data.raw")))
	if err == nil || !strings.Contains(err.Error(), "external data") {
		t.Errorf("external data: got %v, want refusal", err)
	}
	// Reading past the cluster / file ends the scan without error.
	if err := scanQCOWExtensions(bytes.NewReader(nil), cs-4, cs, cs); err != nil {
		t.Errorf("offset past cluster: %v", err)
	}
	if err := scanQCOWExtensions(bytes.NewReader(make([]byte, 4)), 0, cs, 4); err != nil {
		t.Errorf("offset past file: %v", err)
	}
}
