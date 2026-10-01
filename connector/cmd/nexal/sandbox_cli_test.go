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
