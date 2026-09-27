package main

import (
	"context"
	"nexal/connector/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type guestGuardRunner struct {
	called chan []string
	cancel context.CancelFunc
}

func (r guestGuardRunner) Run(_ context.Context, name string, args ...string) error {
	r.called <- append([]string{name}, args...)
	r.cancel()
	return nil
}
func TestGuestGuardDisconnectsWithoutUserConfigOrNetwork(t *testing.T) {
	for _, mode := range []string{"deadline", "reboot_expired", "rollback"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			start := now.Add(-time.Minute)
			end := now.Add(30 * time.Millisecond)
			if mode == "reboot_expired" {
				end = now.Add(-time.Second)
			}
			if mode == "rollback" {
				start = now.Add(time.Minute)
				end = now.Add(2 * time.Minute)
			}
			grant := config.GuestAccess{GrantID: "grant_1", ReceivedAt: start.UTC().Format(time.RFC3339Nano), AccessExpiresAt: end.UTC().Format(time.RFC3339Nano), InviterEmail: "owner@example.com"}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			calls := make(chan []string, 1)
			_ = runGuestGuard(ctx, filepath.Join(t.TempDir(), "removed-config.json"), grant, guestGuardRunner{calls, cancel})
			select {
			case args := <-calls:
				if strings.Join(args, " ") != "nexal-network down" {
					t.Fatal("unexpected privileged command", args)
				}
			default:
				t.Fatal("expired guest retained network access")
			}
		})
	}
}
func TestGuestGuardDefinitionPinsRootHelperAndEscapesConfig(t *testing.T) {
	now := time.Now()
	g := config.GuestAccess{GrantID: "g", ReceivedAt: now.Format(time.RFC3339Nano), AccessExpiresAt: now.Add(time.Hour).Format(time.RFC3339Nano), InviterEmail: "owner@example.com"}
	raw := string(guestGuardDefinition(guestGuardExecutable, "/Users/person/a&b/config.json", g))
	if !strings.Contains(raw, "/Library/PrivilegedHelperTools/neXalGuest/nexal") || !strings.Contains(raw, "a&amp;b") || !strings.Contains(raw, "<key>RunAtLoad</key><true/>") || !strings.Contains(raw, "<key>KeepAlive</key><true/>") {
		t.Fatal("unsafe/nonpersistent root guard definition")
	}
	if strings.Contains(raw, "/bin/sh") || strings.Contains(raw, "PATH") {
		t.Fatal("root helper gained shell or ambient executable lookup")
	}
}
func TestGuestGuardRejectsUserOwnedExecutables(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test exercises unprivileged ownership")
	}
	path := filepath.Join(t.TempDir(), "nexal")
	os.WriteFile(path, []byte("untrusted"), 0755)
	if rootOwnedImmutablePath(path) {
		t.Fatal("root daemon accepted user-controlled code")
	}
	alias := path + "-link"
	os.Symlink(path, alias)
	if rootOwnedImmutablePath(alias) {
		t.Fatal("root daemon accepted symlink")
	}
}

func TestGuestJoinRejectsLateOrExpiredLease(t *testing.T) {
	now := time.Now()
	g := config.GuestAccess{GrantID: "g", ReceivedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), AccessExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), InviterEmail: "owner@example.com"}
	if _, err := guestJoinDeadline(g, now); err != nil {
		t.Fatal(err)
	}
	for _, left := range []time.Duration{35 * time.Second, time.Second, -time.Second} {
		g.AccessExpiresAt = now.Add(left).Format(time.RFC3339Nano)
		if _, err := guestJoinDeadline(g, now); err == nil {
			t.Fatal("accepted unsafe late join", left)
		}
	}
}
