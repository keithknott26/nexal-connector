package mesh

import "testing"

func TestShortNameDropsUpstreamDomain(t *testing.T) {
	for in, want := range map[string]string{
		"keiths-macbook-pro.netbird.cloud": "keiths-macbook-pro",
		"host.":                            "host",
		"plain":                            "plain",
		"":                                 "",
		".leading":                         ".leading", // no label before the dot: keep as-is
	} {
		if got := shortName(in); got != want {
			t.Errorf("shortName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNonNegClampsNegatives(t *testing.T) {
	for in, want := range map[int64]uint64{-5: 0, 0: 0, 7: 7} {
		if got := nonNeg(in); got != want {
			t.Errorf("nonNeg(%d) = %d, want %d", in, got, want)
		}
	}
}
