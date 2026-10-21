package contribution

import "testing"

func TestStateStringsAndUnknownFallback(t *testing.T) {
	power := map[PowerState]string{
		PowerUnknown: "unknown", PowerAC: "ac", PowerBattery: "battery", PowerState(99): "unknown",
	}
	for s, want := range power {
		if got := s.String(); got != want {
			t.Errorf("PowerState(%d).String() = %q, want %q", int(s), got, want)
		}
	}
	thermal := map[ThermalState]string{
		ThermalUnknown: "unknown", ThermalNominal: "nominal", ThermalThrottled: "throttled", ThermalState(99): "unknown",
	}
	for s, want := range thermal {
		if got := s.String(); got != want {
			t.Errorf("ThermalState(%d).String() = %q, want %q", int(s), got, want)
		}
	}
}

func TestJoinAndGibFormatting(t *testing.T) {
	joins := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a and b"},
		{[]string{"a", "b", "c"}, "a, b and c"},
		{[]string{"a", "b", "c", "d"}, "a, b, c and d"},
	}
	for _, c := range joins {
		if got := join(c.in); got != c.want {
			t.Errorf("join(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	gibs := map[uint64]string{
		0:                  "0.0 GiB",
		1 << 30:            "1.0 GiB",
		10<<30 + 4<<28:     "11.0 GiB",
		(10 << 30) + 1<<29: "10.5 GiB",
	}
	for b, want := range gibs {
		if got := gib(b); got != want {
			t.Errorf("gib(%d) = %q, want %q", b, got, want)
		}
	}
}
