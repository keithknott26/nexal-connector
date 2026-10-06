package sandbox

import "testing"

func TestParseAppProgress(t *testing.T) {
	cases := []struct {
		text string
		want AppProgress
		ok   bool
	}{
		{"boot\n", AppProgress{}, false},
		{"[  12.3] NEXAL-APP-STEP app-packages 10\n", AppProgress{Step: "app-packages", Percent: 10}, true},
		{"NEXAL-APP-STEP app-packages 10\nNEXAL-APP-STEP app-download 40\n", AppProgress{Step: "app-download", Percent: 40}, true},
		{"NEXAL-APP-STEP app-start 85\nNEXAL-APP-READY jellyfin\n", AppProgress{Ready: true, Percent: 100}, true},
		{"NEXAL-APP-STEP app-download 40\nNEXAL-APP-FAILED pulling the Jellyfin image\n", AppProgress{Failed: true, Reason: "pulling the Jellyfin image"}, true},
		{"NEXAL-APP-STEP rm-rf 50\nNEXAL-APP-STEP app-start 250\n", AppProgress{}, false},
	}
	for _, c := range cases {
		got, ok := ParseAppProgress(c.text)
		if ok != c.ok || got != c.want {
			t.Errorf("%q: got %+v %v, want %+v %v", c.text, got, ok, c.want, c.ok)
		}
	}
}
