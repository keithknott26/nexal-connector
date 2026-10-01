package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateGuestCommand(t *testing.T) {
	ok := []GuestCommand{{Op: GuestOpVNCPassword, Password: "abcd1234"}, {Op: GuestOpMeshRejoin, Key: "K-1.x"}, {Op: GuestOpMeshStatus}}
	for _, c := range ok {
		if err := ValidateGuestCommand(c); err != nil {
			t.Errorf("%v: %v", c, err)
		}
	}
	bad := []GuestCommand{{Op: "exec"}, {Op: GuestOpVNCPassword, Password: "a b"}, {Op: GuestOpVNCPassword},
		{Op: GuestOpMeshRejoin, Key: "x;rm"}}
	for _, c := range bad {
		if ValidateGuestCommand(c) == nil {
			t.Errorf("%v should be rejected", c)
		}
	}
}

func TestGuestCommandStringHasNoSecret(t *testing.T) {
	s := GuestCommand{Op: GuestOpVNCPassword, Password: "topsecret1"}.String()
	if strings.Contains(s, "topsecret1") {
		t.Fatal("secret leaked")
	}
}

func serveGuest(t *testing.T, reply string) (string, chan GuestCommand) {
	t.Helper()
	dir, err := os.MkdirTemp("", "gc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "g.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan GuestCommand, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		var cmd GuestCommand
		_ = json.Unmarshal(line, &cmd)
		got <- cmd
		_, _ = c.Write([]byte(reply + "\n"))
	}()
	return sock, got
}

func TestSocketGuestChannel(t *testing.T) {
	sock, got := serveGuest(t, `{"ok":true,"connected":true,"meshIp":"100.64.1.7"}`)
	rep, err := SocketGuestChannel{}.Send(context.Background(), Handle{Guest: sock}, GuestCommand{Op: GuestOpMeshStatus})
	if err != nil || !rep.Connected || rep.MeshIP != "100.64.1.7" {
		t.Fatalf("%+v %v", rep, err)
	}
	if c := <-got; c.Op != GuestOpMeshStatus {
		t.Fatalf("server saw %+v", c)
	}
}

func TestSocketGuestChannelRefusalAndBadIP(t *testing.T) {
	sock, _ := serveGuest(t, `{"ok":false,"error":"nope"}`)
	if _, err := (SocketGuestChannel{}).Send(context.Background(), Handle{Guest: sock}, GuestCommand{Op: GuestOpMeshStatus}); err == nil {
		t.Fatal("expected refusal")
	}
	sock, _ = serveGuest(t, `{"ok":true,"meshIp":"not-an-ip"}`)
	rep, err := SocketGuestChannel{}.Send(context.Background(), Handle{Guest: sock}, GuestCommand{Op: GuestOpMeshStatus})
	if err != nil || rep.MeshIP != "" {
		t.Fatalf("untrusted ip must be dropped: %+v %v", rep, err)
	}
	if _, err := (SocketGuestChannel{}).Send(context.Background(), Handle{}, GuestCommand{Op: GuestOpMeshStatus}); err == nil {
		t.Fatal("no channel must error")
	}
}
