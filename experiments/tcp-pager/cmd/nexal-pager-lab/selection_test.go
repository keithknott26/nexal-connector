package main

import (
	"bytes"
	"strings"
	"testing"

	"nexal/experiments/tcp-pager/lab"
)

func TestNumberedConnectionSelection(t *testing.T) {
	options := []lab.NetworkAddress{
		{IP: "192.168.1.20", Interface: "en0", ConnectionType: "Ethernet (wired)"},
		{IP: "192.168.1.30", Interface: "en1", ConnectionType: "Wi-Fi"},
		{IP: "fd00::2", Interface: "en1", ConnectionType: "Wi-Fi"},
	}
	for _, tc := range []struct{ choice, want string }{
		{"1\n", "192.168.1.20:9443"}, {"2\n", "192.168.1.30:9443"},
		{"3\n", "[fd00::2]:9443"}, {"0\n99\nx\n\n 1 \n", "192.168.1.20:9443"},
	} {
		var output bytes.Buffer
		got, err := chooseEndpoint(options, strings.NewReader(tc.choice), &output)
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %q err %v", tc.choice, got, err)
		}
		for _, label := range []string{"1) 192.168.1.20 | Ethernet (wired) | en0",
			"2) 192.168.1.30 | Wi-Fi | en1", "3) fd00::2 | Wi-Fi | en1",
			"Choose a connection (1-3)", "Selected donor connection:"} {
			if !strings.Contains(output.String(), label) {
				t.Fatalf("missing %q in %s", label, output.String())
			}
		}
	}
}

func TestSingleConnectionStillRequiresChoice(t *testing.T) {
	options := []lab.NetworkAddress{{IP: "192.168.1.20", Interface: "en0", ConnectionType: "Ethernet (wired)"}}
	var output bytes.Buffer
	if _, err := chooseEndpoint(options, strings.NewReader(""), &output); err == nil {
		t.Fatal("single interface silently selected")
	}
	if !strings.Contains(output.String(), "1) ") || !strings.Contains(output.String(), "(1-1)") {
		t.Fatal(output.String())
	}
	if _, err := chooseEndpoint(options, strings.NewReader("1\n"), &output); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionCancellationAndBounds(t *testing.T) {
	options := []lab.NetworkAddress{{IP: "192.168.1.20", Interface: "en0", ConnectionType: "Ethernet (wired)"}}
	for _, choice := range []string{"q\n", "Q\n", "", "wrong\n", strings.Repeat("1", 5000)} {
		var output bytes.Buffer
		if got, err := chooseEndpoint(options, strings.NewReader(choice), &output); err == nil || got != "" {
			t.Fatalf("%q unexpectedly selected", choice[:min(len(choice), 12)])
		}
		if strings.Contains(output.String(), "Selected donor connection:") {
			t.Fatal("selection claimed on failure")
		}
	}
	var output bytes.Buffer
	if _, err := chooseEndpoint(nil, strings.NewReader("1\n"), &output); err == nil {
		t.Fatal("accepted empty interface list")
	}
}
