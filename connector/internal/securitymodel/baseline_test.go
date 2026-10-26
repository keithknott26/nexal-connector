package securitymodel

import (
	"errors"
	"testing"
	"time"
)

func TestBaselineRequiresReviewBeforeActivationAndComparesEveryWindow(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s, err := NewBaseline(start, 14*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clean := Aggregate{Samples: 100, FeatureCounts: map[string]uint64{"process_exec": 10, "network_egress": 2}}
	for _, w := range []Window{WindowWeekly, WindowMonthly, WindowYearly} {
		if err := s.Observe(start.Add(time.Hour), w, clean, false); err != nil {
			t.Fatalf("observe %s: %v", w, err)
		}
	}
	if err := s.Observe(start.Add(15*24*time.Hour), WindowWeekly, clean, false); !errors.Is(err, ErrReviewRequired) || s.Phase != PhaseReviewRequired {
		t.Fatalf("observation completion bypassed review: phase=%s err=%v", s.Phase, err)
	}
	if err := s.Activate(); !errors.Is(err, ErrReviewRequired) {
		t.Fatal("baseline activated without approval")
	}
	if err := s.ApproveClean(start.Add(16*24*time.Hour), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Activate(); err != nil {
		t.Fatal(err)
	}
	for _, w := range []Window{WindowWeekly, WindowMonthly, WindowYearly} {
		d, err := s.Compare(w, Aggregate{Samples: 10, FeatureCounts: map[string]uint64{"network_egress": 2}})
		if err != nil || d.Maximum != 10 {
			t.Fatalf("compare %s: %+v err=%v", w, d, err)
		}
	}
}

func TestDetectedThreatIsHeldOutOfTraining(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	s, _ := NewBaseline(start, 7*24*time.Hour)
	before := s.Baselines[WindowWeekly].Samples
	err := s.Observe(start.Add(time.Hour), WindowWeekly, Aggregate{Samples: 50, FeatureCounts: map[string]uint64{"credential_read": 50}}, true)
	if !errors.Is(err, ErrReviewRequired) || s.Baselines[WindowWeekly].Samples != before || s.ThreatsHeld != 1 {
		t.Fatalf("threat entered clean baseline: %+v err=%v", s, err)
	}
}

func TestAggregateRejectsPrivacyUnsafeFeatureNames(t *testing.T) {
	s, _ := NewBaseline(time.Now(), 7*24*time.Hour)
	for _, name := range []string{"/Users/alice/.ssh/id", "api.example.com", "TOKEN=value", ""} {
		err := s.Observe(time.Now(), WindowWeekly, Aggregate{Samples: 1, FeatureCounts: map[string]uint64{name: 1}}, false)
		if err == nil {
			t.Fatalf("privacy-unsafe feature accepted: %q", name)
		}
	}
}

func TestWindowBoundsAreUTCCanonicalAndReplaySafe(t *testing.T) {
	asOf := time.Date(2026, 3, 8, 1, 59, 0, 0, time.FixedZone("EST", -5*60*60))
	tests := map[Window][2]string{
		WindowWeekly:  {"2026-02-23T00:00:00Z", "2026-03-02T00:00:00Z"},
		WindowMonthly: {"2026-02-01T00:00:00Z", "2026-03-01T00:00:00Z"},
		WindowYearly:  {"2025-01-01T00:00:00Z", "2026-01-01T00:00:00Z"},
	}
	for window, want := range tests {
		start, end, err := WindowBounds(window, asOf)
		if err != nil || start.Format(time.RFC3339) != want[0] || end.Format(time.RFC3339) != want[1] {
			t.Fatalf("%s bounds %s..%s err=%v", window, start, end, err)
		}
	}
	s, _ := NewBaseline(asOf.Add(-8*24*time.Hour), 14*24*time.Hour)
	a := Aggregate{Samples: 1, FeatureCounts: map[string]uint64{"process_exec": 1}}
	if err := s.Observe(asOf, WindowWeekly, a, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(asOf.Add(time.Hour), WindowWeekly, a, false); err == nil {
		t.Fatal("replayed completed UTC window was trained twice")
	}
}

func TestWindowBoundsRejectInvalidInputAndHandleSundayBoundary(t *testing.T) {
	if _, _, err := WindowBounds(Window("daily"), time.Now()); err == nil {
		t.Fatal("unknown window accepted")
	}
	if _, _, err := WindowBounds(WindowWeekly, time.Time{}); err == nil {
		t.Fatal("zero time accepted")
	}
	// Sunday 2026-03-08 belongs to the week starting Monday 2026-03-02.
	start, end, err := WindowBounds(WindowWeekly, time.Date(2026, 3, 8, 23, 0, 0, 0, time.UTC))
	if err != nil || start.Format(time.RFC3339) != "2026-02-23T00:00:00Z" || end.Format(time.RFC3339) != "2026-03-02T00:00:00Z" {
		t.Fatalf("sunday bounds %s..%s err=%v", start, end, err)
	}
}

func TestNewBaselineRejectsOutOfRangeObservation(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{0, 6 * 24 * time.Hour, 53 * 7 * 24 * time.Hour} {
		if _, err := NewBaseline(now, d); err == nil {
			t.Fatalf("duration %s accepted", d)
		}
	}
	if _, err := NewBaseline(time.Time{}, 14*24*time.Hour); err == nil {
		t.Fatal("zero time accepted")
	}
	if _, err := NewBaseline(now, 52*7*24*time.Hour); err != nil {
		t.Fatalf("upper bound rejected: %v", err)
	}
}
