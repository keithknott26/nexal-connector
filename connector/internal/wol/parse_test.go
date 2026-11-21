package wol

import (
	"reflect"
	"testing"
)

func TestParseWompCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"enabled", "System-wide power settings:\n sleep 1\n womp   1\n", WakeEnabled},
		{"disabled", " womp 0\n", WakeDisabled},
		{"missing", " sleep 1\n displaysleep 10\n", WakeUnknown},
		{"duplicate lines are ambiguous", " womp 1\n womp 0\n", WakeUnknown},
		{"other value", " womp 2\n", WakeUnknown},
		{"trailing text rejected", " womp 1 extra\n", WakeUnknown},
		{"empty", "", WakeUnknown},
	}
	for _, c := range cases {
		if got := ParseWomp([]byte(c.in)); got != c.want {
			t.Errorf("%s: ParseWomp = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestParseHardwarePortsCases(t *testing.T) {
	in := "Hardware Port: Wi-Fi\nDevice: en0\nEthernet Address: A0:BB:CC:DD:EE:01\n\n" +
		"Hardware Port: Thunderbolt Bridge\nDevice: bridge0\nEthernet Address: N/A\n\n" +
		"Hardware Port: Locally administered\nDevice: en9\nEthernet Address: 02:11:22:33:44:55\n\n" +
		"Hardware Port: Multicast\nDevice: en8\nEthernet Address: 01:00:5e:00:00:01\n\n" +
		"Hardware Port: Ethernet\nDevice: en5\nEthernet Address: 3c:22:fb:01:02:03\n"
	want := []string{"a0:bb:cc:dd:ee:01", "3c:22:fb:01:02:03"}
	if got := ParseHardwarePorts([]byte(in)); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseHardwarePorts = %v, want %v", got, want)
	}
	if got := ParseHardwarePorts(nil); got != nil {
		t.Fatalf("ParseHardwarePorts(nil) = %v, want nil", got)
	}
}
