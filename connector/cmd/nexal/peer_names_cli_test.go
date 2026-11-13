package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"nexal/connector/internal/config"
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

type scriptedPeerNames struct {
	body json.RawMessage
	err  error
}

func (s scriptedPeerNames) PeerNames(context.Context, string) (json.RawMessage, error) {
	return s.body, s.err
}
func (s scriptedPeerNames) SetPeerName(context.Context, string, string, string) (json.RawMessage, error) {
	return s.body, s.err
}

func TestPeerNamesListWritesBodyAndDefaultsEmptyToObject(t *testing.T) {
	var out bytes.Buffer
	api := scriptedPeerNames{body: json.RawMessage(`{"names":[{"address":"100.86.1.1","name":"Lab"}]}`)}
	if err := runPeerNames(context.Background(), api, "host_1", "list", "", strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != `{"names":[{"address":"100.86.1.1","name":"Lab"}]}`+"\n" {
		t.Fatalf("unexpected list output %q", got)
	}
	out.Reset()
	if err := runPeerNames(context.Background(), scriptedPeerNames{}, "host_1", "list", "", strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "{}\n" {
		t.Fatalf("empty body must print {}, got %q", got)
	}
}

func TestPeerNamesPropagatesAPIErrorWithoutWriting(t *testing.T) {
	boom := errors.New("coordinator unreachable")
	for _, action := range []string{"list", "set"} {
		var out bytes.Buffer
		address := ""
		if action == "set" {
			address = "100.86.1.1"
		}
		err := runPeerNames(context.Background(), scriptedPeerNames{err: boom}, "host_1", action, address, strings.NewReader("Lab\n"), &out)
		if !errors.Is(err, boom) || out.Len() != 0 {
			t.Fatalf("%s: err=%v output=%q", action, err, out.String())
		}
	}
}

func TestPeerNamesInvalidNameHasStableCode(t *testing.T) {
	err := runPeerNames(context.Background(), scriptedPeerNames{}, "host_1", "set", "100.86.1.1", strings.NewReader("bad\x07"), &bytes.Buffer{})
	if errorCode(err) != "invalid_name" {
		t.Fatalf("expected invalid_name, got %v", err)
	}
}

func TestPeerNamesCommandRejectsBadArgsBeforeReadingConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent", "config.json")
	for name, args := range map[string][]string{
		"no action":         {"--config", missing},
		"list with address": {"--action", "list", "--address", "100.64.0.2", "--config", missing},
		"set bad address":   {"--action", "set", "--address", "8.8.8.8", "--config", missing},
	} {
		err := peerNamesCommand(context.Background(), args)
		if err == nil || errorCode(err) != "invalid_arguments" {
			t.Errorf("%s: want invalid_arguments, got %v", name, err)
		}
	}
}

func TestPeerNamesCommandRequiresEnrolledHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	c := config.Config{
		Version: 1, Coordinator: "http://127.0.0.1:1", Name: "M2 mini", Listen: "127.0.0.1:8788",
		Development: true, Paused: true, MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 4096 << 20, IdleSeconds: 300,
	}
	if err := config.Save(path, c); err != nil {
		t.Fatal(err)
	}
	err := peerNamesCommand(context.Background(), []string{"--action", "list", "--config", path})
	if err == nil || errorCode(err) != "not_enrolled" {
		t.Fatalf("unenrolled host: want not_enrolled, got %v", err)
	}
}
