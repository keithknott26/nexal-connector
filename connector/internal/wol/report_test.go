package wol

import "testing"

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
