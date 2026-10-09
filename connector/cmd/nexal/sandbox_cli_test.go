package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"nexal/connector/internal/client"
)

type fakeSandboxAPI struct {
	listErr, connErr error
	gotID, gotKind   string
	gotHost          string
	gotKey           string
}

func (f *fakeSandboxAPI) ListSandboxes(_ context.Context, hostID string) (json.RawMessage, error) {
	f.gotHost = hostID
	return json.RawMessage(`{"sandboxes":[{"id":"sb1","newField":true}]}`), f.listErr
}
func (f *fakeSandboxAPI) ConnectSandbox(_ context.Context, hostID, id, kind, key string) (json.RawMessage, error) {
	f.gotHost, f.gotID, f.gotKind, f.gotKey = hostID, id, kind, key
	return json.RawMessage(`{"kind":"` + kind + `","host":"100.64.0.2","pending":false}`), f.connErr
}

func (f *fakeSandboxAPI) CreateSandbox(_ context.Context, hostID string, body json.RawMessage) (json.RawMessage, error) {
	f.gotHost, f.gotKey = hostID, string(body)
	return json.RawMessage(`{"id":"sb2","state":"requested"}`), nil
}
func (f *fakeSandboxAPI) DeleteSandbox(_ context.Context, hostID, id string) (json.RawMessage, error) {
	f.gotHost, f.gotID = hostID, id
	return json.RawMessage(`{"id":"` + id + `","state":"stopping"}`), nil
}
func (f *fakeSandboxAPI) SetSandboxPower(_ context.Context, hostID, id, action string) (json.RawMessage, error) {
	f.gotHost, f.gotID, f.gotKind = hostID, id, action
	return json.RawMessage(`{"id":"` + id + `","state":"` + action + `ping"}`), nil
}
func (f *fakeSandboxAPI) SandboxRunners(_ context.Context, hostID string) (json.RawMessage, error) {
	return json.RawMessage(`{"runners":[]}`), nil
}

func (f *fakeSandboxAPI) SandboxImages(_ context.Context, hostID, runner string) (json.RawMessage, error) {
	f.gotHost, f.gotID = hostID, runner
	return json.RawMessage(`{"images":[]}`), nil
}

func TestSandboxCLICreateAndImages(t *testing.T) {
	ctx := context.Background()
	api := &fakeSandboxAPI{}
	var out bytes.Buffer
	req := `{"imageId":"ubuntu-24.04-arm64","runnerHostId":"host1","kind":"devcontainer"}`
	if err := runSandbox(ctx, api, "host1", "create", "", "", false, strings.NewReader(req+"\n"), &out); err != nil ||
		api.gotKey != req || !strings.Contains(out.String(), `"sb2"`) {
		t.Fatalf("create: %q %v", out.String(), err)
	}
	if err := runSandbox(ctx, api, "host1", "create", "", "", false, strings.NewReader("not json"), &out); err == nil {
		t.Fatal("create accepted a non-JSON body")
	}
	out.Reset()
	if err := runSandbox(ctx, api, "host1", "images", "", "", false, strings.NewReader(""), &out); err != nil || api.gotID != "" || out.String() != `{"images":[]}`+"\n" {
		t.Fatalf("images: %q %v", out.String(), err)
	}
	if err := runSandbox(ctx, api, "host1", "images", "", "ssh", false, strings.NewReader(""), &out); err == nil {
		t.Fatal("images accepted --kind")
	}
}

func TestSandboxCLI(t *testing.T) {
	ctx := context.Background()
	api := &fakeSandboxAPI{}
	var out bytes.Buffer
	if err := runSandbox(ctx, api, "host1", "list", "", "", false, strings.NewReader(""), &out); err != nil ||
		out.String() != `{"sandboxes":[{"id":"sb1","newField":true}]}`+"\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	out.Reset()
	if err := runSandbox(ctx, api, "host1", "connect", "sb1", "ssh", true, strings.NewReader("ssh-ed25519 AAAA me@mac\n"), &out); err != nil ||
		api.gotKey != "ssh-ed25519 AAAA me@mac" || api.gotHost != "host1" || api.gotID != "sb1" || api.gotKind != "ssh" || !strings.Contains(out.String(), `"host":"100.64.0.2"`) {
		t.Fatalf("%q %v %+v", out.String(), err, api)
	}
	out.Reset()
	if err := runSandbox(ctx, api, "host1", "connect", "sb1", "vnc", false, strings.NewReader("ignored"), &out); err != nil || api.gotKey != "" {
		t.Fatalf("vnc sends no key: %v %+v", err, api)
	}

	codeOf := func(err error) string {
		var c *codedError
		if errors.As(err, &c) {
			return c.code
		}
		return "?"
	}
	for name, tc := range map[string]struct {
		action, id, kind string
		stdin            bool
		in               string
		code             string
	}{
		"no action":   {"", "", "", false, "", "invalid_arguments"},
		"list + id":   {"list", "sb1", "", false, "", "invalid_arguments"},
		"bad id":      {"connect", "../x", "ssh", true, "ssh-ed25519 A", "invalid_arguments"},
		"bad kind":    {"connect", "sb1", "rdp", false, "", "invalid_arguments"},
		"ssh no key":  {"connect", "sb1", "ssh", false, "", "public_key_required"},
		"rsa key":     {"connect", "sb1", "files", true, "ssh-rsa AAAA", "invalid_public_key"},
		"two lines":   {"connect", "sb1", "ssh", true, "ssh-ed25519 A\nssh-ed25519 B", "invalid_public_key"},
		"empty stdin": {"connect", "sb1", "ssh", true, "", "invalid_public_key"},
	} {
		err := runSandbox(ctx, &fakeSandboxAPI{}, "host1", tc.action, tc.id, tc.kind, tc.stdin, strings.NewReader(tc.in), &bytes.Buffer{})
		if err == nil || codeOf(err) != tc.code {
			t.Errorf("%s: %v (%s), want %s", name, err, codeOf(err), tc.code)
		}
	}
	// coordinator error codes pass through
	bad := &fakeSandboxAPI{connErr: &client.StatusError{Status: 409, Code: "sandbox_not_running"}}
	if err := runSandbox(ctx, bad, "host1", "connect", "sb1", "vnc", false, nil, &out); codeOf(err) != "sandbox_not_running" {
		t.Fatalf("%v", err)
	}
	if err := runSandbox(ctx, &fakeSandboxAPI{listErr: &client.StatusError{Status: 429}}, "host1", "list", "", "", false, nil, &out); codeOf(err) != "rate_limited" {
		t.Fatalf("%v", err)
	}
}

func TestSandboxCLIDelete(t *testing.T) {
	ctx := context.Background()
	api := &fakeSandboxAPI{}
	var out bytes.Buffer
	if err := runSandbox(ctx, api, "host1", "delete", "sb1", "", false, strings.NewReader(""), &out); err != nil ||
		api.gotHost != "host1" || api.gotID != "sb1" || !strings.Contains(out.String(), `"stopping"`) {
		t.Fatalf("delete: %q %v", out.String(), err)
	}
	for _, bad := range [][3]string{{"", "", ""}, {"../x", "", ""}, {"sb1", "vnc", ""}} {
		if err := checkSandboxArgs("delete", bad[0], bad[1], false); err == nil {
			t.Fatalf("delete accepted %v", bad)
		}
	}
	if err := checkSandboxArgs("delete", "sb1", "", true); err == nil {
		t.Fatal("delete accepted --public-key-stdin")
	}
	if err := checkSandboxArgs("delete", "sb1", "", false); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxCLIStopStart(t *testing.T) {
	ctx := context.Background()
	for _, action := range []string{"stop", "start"} {
		api := &fakeSandboxAPI{}
		var out bytes.Buffer
		if err := runSandbox(ctx, api, "host1", action, "sb1", "", false, strings.NewReader(""), &out); err != nil ||
			api.gotHost != "host1" || api.gotID != "sb1" || api.gotKind != action {
			t.Fatalf("%s: %q %v", action, out.String(), err)
		}
		if err := checkSandboxArgs(action, "sb1", "", false); err != nil {
			t.Fatal(err)
		}
		if err := checkSandboxArgs(action, "", "", false); err == nil {
			t.Fatalf("%s accepted no id", action)
		}
		if err := checkSandboxArgs(action, "sb1", "vnc", false); err == nil {
			t.Fatalf("%s accepted --kind", action)
		}
	}
}
