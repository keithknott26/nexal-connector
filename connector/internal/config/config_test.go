package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoordinatorURLFailClosed(t *testing.T) {
	for _, raw := range []string{
		"http://example.com", "http://localhost:8787", "http://127.0.0.1.evil.test",
		"ftp://127.0.0.1", "https://user:secret@example.com", "https://example.com?token=secret",
		"https://example.com/a", "https://example.com/#fragment", "//example.com",
		"https://example.com\\@evil.test", "https://", "https://example.com/%2e%2e",
	} {
		t.Run(raw, func(t *testing.T) {
			if ValidateURL(raw, true) == nil {
				t.Fatal("unsafe URL accepted")
			}
		})
	}
	if ValidateURL("http://127.0.0.1:8787", false) == nil {
		t.Fatal("production plaintext accepted")
	}
	for _, raw := range []string{"https://coordinator.example", "https://127.0.0.1:8443"} {
		if err := ValidateURL(raw, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:8787", "http://[::1]:8787"} {
		if err := ValidateURL(raw, true); err != nil {
			t.Fatal(err)
		}
	}
}
func TestListenLoopbackOnly(t *testing.T) {
	for _, raw := range []string{"0.0.0.0:8788", ":8788", "localhost:8788", "10.0.0.1:8788", "[::]:8788", "127.0.0.1:http", "127.0.0.1:0"} {
		if ValidateListen(raw) == nil {
			t.Errorf("accepted unsafe listen: %s", raw)
		}
	}
	if err := ValidateListen("[::1]:8788"); err != nil {
		t.Fatal(err)
	}
}
func TestAtomicPrivateAndSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "private", "config.json")
	for _, b := range []string{"first", "second"} {
		if err := AtomicPrivate(path, []byte(b)); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("mode %o", info.Mode().Perm())
		}
		got, err := ReadPrivate(path, 100)
		if err != nil || string(got) != b {
			t.Fatalf("%s %v", got, err)
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivate(path, 100); err == nil {
		t.Fatal("insecure secret read accepted")
	}
	if err := AtomicPrivate(path, []byte("x")); err == nil {
		t.Fatal("insecure overwrite accepted")
	}
	s := FileSecrets{Dir: filepath.Join(dir, "secrets")}
	token, _ := RandomToken()
	if err := s.Put(context.Background(), "host", token); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), "host")
	if err != nil || got != token {
		t.Fatal("secret round trip failed")
	}
	if s.Put(context.Background(), "../host", token) == nil {
		t.Fatal("credential traversal accepted")
	}
	if s.Put(context.Background(), "host", "bad\nsecret") == nil {
		t.Fatal("malformed secret accepted")
	}
}
func TestSymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "private", "real")
	if err := AtomicPrivate(target, []byte("private")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivate(link, 100); err == nil {
		t.Fatal("symlink read")
	}
	if err := AtomicPrivate(link, []byte("bad")); err == nil {
		t.Fatal("symlink write")
	}
}
func TestKeychainQuote(t *testing.T) {
	q := keychainQuote(`ab"cd\ef`)
	if q != `"ab\"cd\\ef"` || strings.Contains(q, "\n") {
		t.Fatal(q)
	}
}
func TestExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if again, err := Lock(path); err == nil {
		again()
		t.Fatal("second connector lock succeeded")
	}
}
