package remoteinference

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestForwarderSwapsServersAndSaysWhenThereIsNone(t *testing.T) {
	one := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "one "+r.URL.Path) }))
	defer one.Close()
	two := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "two") }))
	defer two.Close()
	addr := freeAddr(t)
	f, err := NewForwarder(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if code, body := get(t, "http://"+addr+"/v1/models"); code != 503 || !strings.Contains(body, "starting") {
		t.Fatal(code, body)
	}
	f.SetUpstream(strings.TrimPrefix(one.URL, "http://"), "")
	if code, body := get(t, "http://"+addr+"/v1/models"); code != 200 || body != "one /v1/models" {
		t.Fatal(code, body)
	}
	f.SetUpstream(strings.TrimPrefix(two.URL, "http://"), "")
	if _, body := get(t, "http://"+addr+"/x"); body != "two" {
		t.Fatal(body)
	}
	f.SetUpstream("", "The pool lost a member.")
	if code, body := get(t, "http://"+addr+"/x"); code != 503 || !strings.Contains(body, "lost a member") {
		t.Fatal(code, body)
	}
	if _, err := NewForwarder(addr); err == nil {
		t.Fatal("a second forwarder on the same address should fail")
	}
	// A server that went away is a clear 502, not a hang.
	f.SetUpstream(freeAddr(t), "")
	if code, body := get(t, "http://"+addr+"/x"); code != 502 || !strings.Contains(body, "stopped answering") {
		t.Fatal(code, body)
	}
	if n, quiet := f.Busy(); n != 0 || quiet > time.Minute {
		t.Fatal(n, quiet)
	}
}

func TestForwarderAlsoListensOnExtraAddresses(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer up.Close()
	a, b := freeAddr(t), freeAddr(t)
	f, err := NewForwarder(a, b)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.SetUpstream(strings.TrimPrefix(up.URL, "http://"), "")
	for _, addr := range []string{a, b} {
		if code, body := get(t, "http://"+addr+"/v1/models"); code != 200 || body != "ok" {
			t.Fatal(addr, code, body)
		}
	}
	if _, err := NewForwarder(freeAddr(t), a); err == nil {
		t.Fatal("an extra address that is taken should fail")
	}
}
