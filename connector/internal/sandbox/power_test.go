package sandbox

import (
	"testing"
	"time"
)

func TestSleepGap(t *testing.T) {
	if g := SleepGap(65*time.Second, 5*time.Second, 15*time.Second); g != 60*time.Second {
		t.Fatalf("gap %v", g)
	}
	if g := SleepGap(5*time.Second+200*time.Millisecond, 5*time.Second, 15*time.Second); g != 0 {
		t.Fatalf("jitter must not count: %v", g)
	}
	if g := SleepGap(3*time.Second, 5*time.Second, 15*time.Second); g != 0 {
		t.Fatalf("negative gap: %v", g)
	}
}

func TestSleepDetectorNoGapWhenAwake(t *testing.T) {
	var d SleepDetector
	if g := d.Check(time.Now()); g != 0 {
		t.Fatal("first call")
	}
	time.Sleep(10 * time.Millisecond)
	if g := d.Check(time.Now()); g != 0 {
		t.Fatalf("awake host reported gap %v", g)
	}
}

func TestParseLidClosed(t *testing.T) {
	yes := "+-o Root\n    | {\n    |   \"AppleClamshellState\" = Yes\n    | }\n"
	no := "    |   \"AppleClamshellState\" = No\n"
	if c, ok := ParseLidClosed(yes); !c || !ok {
		t.Fatal("yes")
	}
	if c, ok := ParseLidClosed(no); c || !ok {
		t.Fatal("no")
	}
	if _, ok := ParseLidClosed("nothing here"); ok {
		t.Fatal("desktop Mac has no lid")
	}
}

func TestPowerTracker(t *testing.T) {
	var p PowerTracker
	t0 := time.Unix(1000, 0)
	if a := p.Observe(false, t0); a != PowerNone {
		t.Fatal("open lid")
	}
	if a := p.Observe(true, t0.Add(time.Second)); a != PowerAnnounceSleep {
		t.Fatal("close should announce sleep")
	}
	if a := p.Observe(true, t0.Add(5*time.Second)); a != PowerNone {
		t.Fatal("still inside grace")
	}
	// Mac stayed awake (clamshell mode): take the announcement back once.
	if a := p.Observe(true, t0.Add(30*time.Second)); a != PowerAnnounceAwake {
		t.Fatal("grace over, still awake")
	}
	if a := p.Observe(true, t0.Add(40*time.Second)); a != PowerNone {
		t.Fatal("must not re-announce while the lid stays shut")
	}
	if a := p.Observe(false, t0.Add(50*time.Second)); a != PowerNone {
		t.Fatal("opening after a taken-back announcement is silent")
	}
	// Normal path: close, sleep, wake, open.
	if a := p.Observe(true, t0.Add(60*time.Second)); a != PowerAnnounceSleep {
		t.Fatal("second close")
	}
	p.Woke()
	if a := p.Observe(false, t0.Add(900*time.Second)); a != PowerNone {
		t.Fatal("wake already reported awake")
	}
}
