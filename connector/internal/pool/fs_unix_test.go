//go:build linux || darwin

package pool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTestRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := testDirectory(t)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, dir
}

func TestOpenNoFollowAcceptsPrivateRegularFile(t *testing.T) {
	root, dir := openTestRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "ok"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := openNoFollow(root, "ok", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("private regular file rejected: %v", err)
	}
	f.Close()
}

func TestOpenNoFollowRejectsUnsafeFiles(t *testing.T) {
	root, dir := openTestRoot(t)
	loose := filepath.Join(dir, "loose")
	if err := os.WriteFile(loose, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := openNoFollow(root, "loose", os.O_RDONLY, 0); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("group/world-readable file: got %v, want ErrUnsafePath", err)
	}

	priv := filepath.Join(dir, "priv")
	if err := os.WriteFile(priv, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(priv, filepath.Join(dir, "hardlink")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := openNoFollow(root, "priv", os.O_RDONLY, 0); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("multiply-linked file: got %v, want ErrUnsafePath", err)
	}

	if err := os.Mkdir(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := openNoFollow(root, "sub", os.O_RDONLY, 0); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("directory: got %v, want ErrUnsafePath", err)
	}
}

func TestOpenNoFollowRejectsSymlinkEscapingRoot(t *testing.T) {
	root, dir := openTestRoot(t)
	outside := filepath.Join(testDirectory(t), "secret")
	if err := os.WriteFile(outside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := openNoFollow(root, "link", os.O_RDONLY, 0); err == nil {
		t.Fatal("symlink escaping the root was followed")
	}
}

func TestLockFileIsExclusive(t *testing.T) {
	dir := testDirectory(t)
	a, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := lockFile(a); err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if err := lockFile(b); err == nil {
		t.Fatal("second lock on the same file succeeded")
	}
	a.Close()
	if err := lockFile(b); err != nil {
		t.Fatalf("lock after release: %v", err)
	}
}

func TestFreeDiskReportsSpace(t *testing.T) {
	f, err := os.Open(testDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n, err := freeDisk(f)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("freeDisk reported zero bytes on a writable temp volume")
	}
}
