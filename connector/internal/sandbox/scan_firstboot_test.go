package sandbox

import "testing"

func TestScanFirstBootNewestLineWins(t *testing.T) {
	text := "boot noise\n" +
		`NEXAL-FIRSTBOOT {"meshIp":"100.64.1.1"}` + "\r\n" +
		"more noise\n" +
		`NEXAL-FIRSTBOOT {"meshIp":"100.64.1.2"}` + "\n" +
		"trailing noise\n"
	fb, ok, failed, _ := scanFirstBoot(text)
	if !ok || failed || fb.MeshIP != "100.64.1.2" {
		t.Fatalf("want newest report 100.64.1.2, got %+v ok=%v failed=%v", fb, ok, failed)
	}
}

func TestScanFirstBootFailureAndNoReport(t *testing.T) {
	_, ok, failed, reason := scanFirstBoot("a\n" + FirstBootFailedPrefix + " no network\nb\n")
	if ok || !failed || reason != "no network" {
		t.Fatalf("want failure 'no network', got ok=%v failed=%v reason=%q", ok, failed, reason)
	}
	// A malformed report line is skipped, falling back to an older valid one.
	fb, ok, _, _ := scanFirstBoot(`NEXAL-FIRSTBOOT {"meshIp":"100.64.1.3"}` + "\n" + `NEXAL-FIRSTBOOT {"meshIp":"bogus"}` + "\n")
	if !ok || fb.MeshIP != "100.64.1.3" {
		t.Fatalf("malformed newest line should be skipped, got %+v ok=%v", fb, ok)
	}
	if _, ok, failed, _ := scanFirstBoot("nothing here\n\n"); ok || failed {
		t.Fatal("no report expected")
	}
}
