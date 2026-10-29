//go:build !darwin

package sysinfo

import "testing"

func TestStatfsReportsVolumeSizes(t *testing.T) {
	total, free, err := statfs(t.TempDir())
	if err != nil {
		t.Fatalf("statfs: %v", err)
	}
	if total == 0 {
		t.Fatal("total = 0, want a positive volume size")
	}
	if free > total {
		t.Fatalf("free %d exceeds total %d", free, total)
	}
}

func TestStatfsMissingPathErrors(t *testing.T) {
	if _, _, err := statfs("/nonexistent/nexal-statfs-test"); err == nil {
		t.Fatal("statfs of a missing path should fail")
	}
}

func TestDataVolumeIsRoot(t *testing.T) {
	if got := dataVolume(); got != "/" {
		t.Fatalf("dataVolume = %q, want /", got)
	}
}
