//go:build unix

package inference

import "testing"

func TestLockInstallMissingRootFails(t *testing.T) {
	if _, err := lockInstall(t.TempDir() + "/does-not-exist"); err == nil {
		t.Fatal("lock in a missing directory must fail")
	}
}
