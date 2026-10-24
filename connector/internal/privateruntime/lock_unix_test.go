//go:build darwin || linux

package privateruntime

import "testing"

func TestLockIsExclusiveUntilReleased(t *testing.T) {
	root := t.TempDir()
	release, err := lock(root)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := lock(root); err == nil {
		second()
		t.Fatal("second lock on a held root must fail")
	}
	release()
	again, err := lock(root)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
}

func TestLockMissingRootFails(t *testing.T) {
	if release, err := lock(t.TempDir() + "/missing"); err == nil {
		release()
		t.Fatal("lock in a nonexistent root must fail")
	}
}
