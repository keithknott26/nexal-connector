package throttle

import (
	"testing"
	"time"
)

// SystemClock is the production Clock; its timer must fire once, and a stopped
// timer must never deliver.
func TestSystemClockTimerFiresAndStops(t *testing.T) {
	var c Clock = SystemClock{}
	if before := time.Now(); c.Now().Before(before) {
		t.Fatal("Now went backwards relative to time.Now")
	}

	ch, stop := c.NewTimer(5 * time.Millisecond)
	defer stop()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timer did not fire")
	}

	ch2, stop2 := c.NewTimer(50 * time.Millisecond)
	stop2()
	select {
	case <-ch2:
		t.Fatal("stopped timer fired")
	case <-time.After(150 * time.Millisecond):
	}
}
