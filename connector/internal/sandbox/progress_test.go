package sandbox

import "testing"

func TestThrottleDropsNoise(t *testing.T) {
	var got []string
	f := throttle(func(s string, p int) { got = append(got, s) })
	f(StepDownload, 5)
	f(StepDownload, 6) // under 3 points: dropped
	f(StepDownload, 9) // within 2 s of the last report: dropped
	f(StepConvert, 60) // new step: always sent
	if len(got) != 2 || got[0] != StepDownload || got[1] != StepConvert {
		t.Fatalf("got %v", got)
	}
}
