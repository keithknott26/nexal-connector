package wol

import (
	"bytes"
	"fmt"
	"net"
	"sort"
	"testing"
)

func TestReportInterfaces(t *testing.T) {
	cases := []struct {
		name string
		rep  Report
		want int
	}{
		{"empty report", Report{}, 0},
		{
			"sent on one interface",
			Report{Attempts: []Attempt{{Interface: "en0", Packets: 2}}},
			1,
		},
		{
			"limited broadcasts are not counted",
			Report{Attempts: []Attempt{{Interface: "en0", Packets: 2, Limited: true}}},
			0,
		},
		{
			"interface with no packets is not counted",
			Report{Attempts: []Attempt{{Interface: "en0", Packets: 0, Error: "boom"}}},
			0,
		},
		{
			"counts directed attempts only, across interfaces",
			Report{Attempts: []Attempt{
				{Interface: "en0", Packets: 2},
				{Interface: "en0", Packets: 2, Limited: true},
				{Interface: "en1", Packets: 1},
				{Interface: "utun3", Packets: 0},
			}},
			2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rep.Interfaces(); got != tc.want {
				t.Fatalf("Interfaces() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMaskMAC(t *testing.T) {
	if got := MaskMAC(net.HardwareAddr{0x3c, 0x22, 0xfb, 0x01, 0x02, 0x03}); got != "xx:xx:xx:01:02:03" {
		t.Fatalf("MaskMAC = %q", got)
	}
	if got := MaskMAC(net.HardwareAddr{1, 2, 3}); got != "invalid" {
		t.Fatalf("MaskMAC(short) = %q, want invalid", got)
	}
}

func TestSetTraceReceivesReport(t *testing.T) {
	t.Cleanup(func() { SetTrace(nil) })
	var got []Report
	SetTrace(func(r Report) { got = append(got, r) })
	emitTrace(Report{MACs: []string{"xx:xx:xx:01:02:03"}})
	if len(got) != 1 || len(got[0].MACs) != 1 {
		t.Fatalf("trace got %v", got)
	}
	SetTrace(nil)
	emitTrace(Report{}) // no callback registered: must not panic
	if len(got) != 1 {
		t.Fatalf("trace fired after being cleared: %v", got)
	}
}

func TestPrefixKeysCapped(t *testing.T) {
	var prefixes []string
	for i := 0; i < MaxPrefixKeys+5; i++ {
		prefixes = append(prefixes, fmt.Sprintf("10.%d.0.0/24", i))
	}
	keys := PrefixKeys(prefixes)
	if len(keys) != MaxPrefixKeys {
		t.Fatalf("got %d keys, want cap %d", len(keys), MaxPrefixKeys)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("keys not sorted")
	}
}

func TestParseOversizedInput(t *testing.T) {
	big := bytes.Repeat([]byte(" womp 1\n"), 40000)
	if got := ParseWomp(big); got != WakeUnknown {
		t.Fatalf("ParseWomp(oversized) = %q, want unknown", got)
	}
	if got := ParseHardwarePorts(big); got != nil {
		t.Fatalf("ParseHardwarePorts(oversized) = %v, want nil", got)
	}
}
