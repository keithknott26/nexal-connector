package mesh

import (
	"slices"
	"strings"
	"testing"
)

func TestPeerNameSerialThenHostname(t *testing.T) {
	cases := map[[2]string]string{
		{"c02xk1abcd12", "Keiths-Mac-mini"}:     "C02XK1ABCD12-Keiths-Mac-mini",
		{"C02XK1ABCD12", "Keith’s MacBook Pro"}: "C02XK1ABCD12-Keith-s-MacBook-Pro",
		{"", "Keiths-MacBook-Pro"}:              "Keiths-MacBook-Pro",
		{"C02XK1ABCD12", ""}:                    "C02XK1ABCD12",
		{" ", "--"}:                             "",
	}
	for in, want := range cases {
		if got := PeerName(in[0], in[1]); got != want {
			t.Errorf("PeerName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
	long := PeerName("C02XK1ABCD12", strings.Repeat("a", 80))
	if len(long) > maxPeerNameLength || strings.HasSuffix(long, "-") {
		t.Fatalf("not a DNS label: %q", long)
	}
}

func TestPlatformSerial(t *testing.T) {
	out := []byte("+-o J416sAP  <class IOPlatformExpertDevice>\n    \"IOPlatformUUID\" = \"X\"\n    \"IOPlatformSerialNumber\" = \"C02XK1ABCD12\"\n")
	if got := platformSerial(out); got != "C02XK1ABCD12" {
		t.Fatalf("got %q", got)
	}
	if got := platformSerial([]byte("nothing")); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestStartupSetsHostnameOnlyWhenKnown(t *testing.T) {
	plan := StartupPlan{SetupKey: "secret", ManagementURL: "https://management.example"}
	args, err := plan.Arguments("/private/tmp/credential")
	if err != nil || slices.Contains(args, "--hostname") {
		t.Fatalf("unexpected hostname flag: %v %v", args, err)
	}
	plan.Hostname = "C02XK1ABCD12-Keiths-Mac-mini"
	args, err = plan.Arguments("/private/tmp/credential")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(args, "--hostname")
	if i < 0 || i+1 >= len(args) || args[i+1] != plan.Hostname {
		t.Fatalf("missing hostname: %v", args)
	}
}
