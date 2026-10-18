package inference

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestForwardAPI(t *testing.T) {
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "hello "+r.URL.Path) }))
	defer back.Close()
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := free.Addr().String()
	free.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ForwardAPI(ctx, addr, strings.TrimPrefix(back.URL, "http://")); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "hello /v1/models" {
		t.Fatalf("got %q", b)
	}
	if err := ForwardAPI(ctx, addr, "127.0.0.1:1"); err == nil {
		t.Fatal("a second forwarder on the same address should fail")
	}
}
