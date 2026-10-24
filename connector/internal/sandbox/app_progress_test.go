package sandbox

import (
	"os"
	"testing"
)

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

// watchAppSetup polls on a 5s ticker, so each case below takes one tick.
func TestWatchAppSetupReportsReadyAndClearsProgress(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	c := &recCoord{}
	withCoord(m, c)
	put(m, &record{ID: "sbA", State: StateRunning, Kind: SandboxVM})
	if err := os.MkdirAll(m.boxDir("sbA"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.consoleLog("sbA"), []byte("NEXAL-APP-STEP app-start 80\nNEXAL-APP-READY jellyfin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.setAppLast("sbA", &AppProgress{Step: "app-start", Percent: 80})
	m.watchAppSetup("sbA")
	reps := c.all()
	if len(reps) != 1 || reps[0].State != StateRunning || reps[0].Step != "" {
		t.Fatalf("%+v", reps)
	}
	if _, ok := m.appLast["sbA"]; ok {
		t.Fatal("ready must forget the remembered step")
	}
}

func TestWatchAppSetupReportsFailureStep(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	c := &recCoord{}
	withCoord(m, c)
	put(m, &record{ID: "sbB", State: StateRunning, Kind: SandboxVM})
	if err := os.MkdirAll(m.boxDir("sbB"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.consoleLog("sbB"), []byte("NEXAL-APP-FAILED pulling the image\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.watchAppSetup("sbB")
	reps := c.all()
	if len(reps) != 1 || reps[0].Step != "app-failed" || reps[0].Percent != 0 {
		t.Fatalf("%+v", reps)
	}
	if m.appLast["sbB"].Step != "app-failed" {
		t.Fatalf("%+v", m.appLast)
	}
}

func TestWatchAppSetupStopsWhenTheHostIsGone(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, &fakeHyp{}, nil, nil)
	c := &recCoord{}
	withCoord(m, c)
	m.setAppLast("gone", &AppProgress{Step: "app-download", Percent: 5})
	m.watchAppSetup("gone")
	if len(c.all()) != 0 {
		t.Fatalf("a vanished host must not be reported: %+v", c.all())
	}
	if _, ok := m.appLast["gone"]; ok {
		t.Fatal("a vanished host must forget its step")
	}
}
