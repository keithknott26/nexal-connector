package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakePeerNames struct{ address, name string }

func (f *fakePeerNames) PeerNames(context.Context, string) (json.RawMessage, error) {
	return json.RawMessage(`{"names":[]}`), nil
}
func (f *fakePeerNames) SetPeerName(_ context.Context, _ string, address, name string) (json.RawMessage, error) {
	f.address, f.name = address, name
	return json.RawMessage(`{"address":"` + address + `","name":"` + name + `"}`), nil
}

func TestPeerNamesSetReadsOneLineAndTrims(t *testing.T) {
	api := &fakePeerNames{}
	var out bytes.Buffer
	if err := runPeerNames(context.Background(), api, "host_1", "set", "100.86.63.60", strings.NewReader("  Build box  \nignored"), &out); err != nil {
		t.Fatal(err)
	}
	if api.address != "100.86.63.60" || api.name != "Build box" {
		t.Fatalf("got %q %q", api.address, api.name)
	}
}

func TestPeerNamesRefusesBadArguments(t *testing.T) {
	api := &fakePeerNames{}
	for _, c := range []struct{ action, address, input string }{
		{"set", "192.168.1.2", "x"},
		{"set", "100.128.0.1", "x"},
		{"list", "100.86.1.1", ""},
		{"rename", "", ""},
		{"set", "100.86.1.1", strings.Repeat("x", 61)},
		{"set", "100.86.1.1", "bad\x07"},
	} {
		if err := runPeerNames(context.Background(), api, "host_1", c.action, c.address, strings.NewReader(c.input), &bytes.Buffer{}); err == nil {
			t.Fatalf("%+v: expected an error", c)
		}
	}
}
