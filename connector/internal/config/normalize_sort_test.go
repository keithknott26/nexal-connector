package config

import "testing"

func TestNormalizeUploadModeDefaultsSilenceToAuto(t *testing.T) {
	if got := NormalizeUploadMode(""); got != UploadModeAuto {
		t.Fatalf("empty mode = %q, want %q", got, UploadModeAuto)
	}
	for _, mode := range []string{UploadModeAuto, UploadModeManual, UploadModeUnlimited, "bogus"} {
		if got := NormalizeUploadMode(mode); got != mode {
			t.Fatalf("NormalizeUploadMode(%q) = %q, want it unchanged", mode, got)
		}
	}
}

func TestSortStaticPeersOrdersByFingerprintAndIsStable(t *testing.T) {
	peers := []StaticPeer{
		{Endpoint: "b1", Fingerprint: fpB},
		{Endpoint: "a1", Fingerprint: fpA},
		{Endpoint: "b2", Fingerprint: fpB},
	}
	SortStaticPeers(peers)
	want := []string{"a1", "b1", "b2"}
	for i, p := range peers {
		if p.Endpoint != want[i] {
			t.Fatalf("position %d = %q, want %q (full: %+v)", i, p.Endpoint, want[i], peers)
		}
	}
	SortStaticPeers(nil)
}
