package agent

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listen unavailable: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func setListen(a *Agent, addr string) {
	a.mu.Lock()
	a.cfg.Listen = addr
	a.mu.Unlock()
}

func TestServeRejectsNonLoopbackListen(t *testing.T) {
	a, _ := testAgent(t)
	setListen(a, "0.0.0.0:8788")
	if err := a.Serve(context.Background(), strings.Repeat("x", 64)); err == nil {
		t.Fatal("expected non-loopback listen to be rejected")
	}
}

func TestServeRejectsShortAdminToken(t *testing.T) {
	a, _ := testAgent(t)
	setListen(a, freeLoopbackAddr(t))
	if err := a.Serve(context.Background(), "short"); err == nil {
		t.Fatal("expected short admin token to be rejected")
	}
}

func TestServeReportsBusyPort(t *testing.T) {
	a, _ := testAgent(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listen unavailable: %v", err)
	}
	defer ln.Close()
	setListen(a, ln.Addr().String())
	err = a.Serve(context.Background(), strings.Repeat("x", 64))
	if err == nil || !strings.Contains(err.Error(), "another connector") {
		t.Fatalf("expected busy-port error, got %v", err)
	}
}

func TestServeAnswersThenShutsDownOnContextCancel(t *testing.T) {
	a, _ := testAgent(t)
	addr := freeLoopbackAddr(t)
	setListen(a, addr)
	token := strings.Repeat("x", 64)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Serve(ctx, token) }()

	var status int
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, _ := http.NewRequest("GET", "http://"+addr+"/v1/status", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			status = resp.StatusCode
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never became reachable: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancel")
	}
}
