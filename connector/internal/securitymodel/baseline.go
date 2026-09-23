// Package securitymodel defines the connector-side safety boundary for local
// behavioral baselines and proprietary detection bundles.
package securitymodel

import (
	"errors"
	"fmt"
	"math"
	"time"
)

type Phase string

const (
	PhaseObserving        Phase = "observing"
	PhaseReviewRequired   Phase = "review_required"
	PhaseApprovedClean    Phase = "approved_clean"
	PhaseActiveComparison Phase = "active_comparison"
)

type Window string

const (
	WindowWeekly  Window = "weekly"
	WindowMonthly Window = "monthly"
	WindowYearly  Window = "yearly"
)

var ErrReviewRequired = errors.New("security baseline requires explicit review")

// Aggregate is deliberately incapable of carrying raw paths, command lines,
// domains, file contents, prompts, credentials or model input. Sensors reduce
// observations to a reviewed feature vocabulary before calling Observe.
type Aggregate struct {
	Samples       uint64            `json:"samples"`
	FeatureCounts map[string]uint64 `json:"featureCounts"`
}

type State struct {
	Phase           Phase                `json:"phase"`
	ObservationAt   time.Time            `json:"observationStartedAt"`
	ObservationFor  time.Duration        `json:"observationFor"`
	Baselines       map[Window]Aggregate `json:"baselines"`
	ThreatsHeld     uint64               `json:"threatsHeld"`
	ApprovedAt      *time.Time           `json:"approvedAt,omitempty"`
	Revision        uint64               `json:"revision"`
	ObservedBuckets map[string]bool      `json:"observedBuckets"`
}

func NewBaseline(now time.Time, observationFor time.Duration) (State, error) {
	if now.IsZero() || observationFor < 7*24*time.Hour || observationFor > 52*7*24*time.Hour {
		return State{}, errors.New("observation period must be between one and fifty-two weeks")
	}
	return State{Phase: PhaseObserving, ObservationAt: now.UTC(), ObservationFor: observationFor,
		Baselines: map[Window]Aggregate{WindowWeekly: {}, WindowMonthly: {}, WindowYearly: {}}, ObservedBuckets: make(map[string]bool)}, nil
}

// Observe accepts only local aggregates. detectedThreat makes the observation
// ineligible for training and forces review; it is never merged into a clean
// baseline, even if a reviewer later dismisses a different finding.
func (s *State) Observe(now time.Time, window Window, aggregate Aggregate, detectedThreat bool) error {
	if s == nil || !validWindow(window) || validateAggregate(aggregate) != nil || now.Before(s.ObservationAt) {
		return errors.New("invalid baseline observation")
	}
	start, end, err := WindowBounds(window, now)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s:%s:%s", window, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if s.ObservedBuckets == nil {
		s.ObservedBuckets = make(map[string]bool)
	}
	if s.ObservedBuckets[key] {
		return errors.New("baseline window already observed")
	}
	s.ObservedBuckets[key] = true
	if detectedThreat {
		s.ThreatsHeld++
		s.Phase = PhaseReviewRequired
		s.Revision++
		return ErrReviewRequired
	}
	if s.Phase != PhaseObserving {
		return ErrReviewRequired
	}
	s.Baselines[window] = merge(s.Baselines[window], aggregate)
	s.Revision++
	if !now.Before(s.ObservationAt.Add(s.ObservationFor)) {
		s.Phase = PhaseReviewRequired
		return ErrReviewRequired
	}
	return nil
}

// ApproveClean is intentionally separate from Activate. A UI/API must record
// an explicit review before comparison can begin.
func (s *State) ApproveClean(now time.Time, reviewedThreats uint64) error {
	if s == nil || s.Phase != PhaseReviewRequired || reviewedThreats != s.ThreatsHeld || now.IsZero() || len(s.Baselines) != 3 {
		return ErrReviewRequired
	}
	for _, w := range []Window{WindowWeekly, WindowMonthly, WindowYearly} {
		if s.Baselines[w].Samples == 0 {
			return errors.New("all baseline windows require clean observations")
		}
	}
	t := now.UTC()
	s.ApprovedAt = &t
	s.Phase = PhaseApprovedClean
	s.Revision++
	return nil
}

func (s *State) Activate() error {
	if s == nil || s.Phase != PhaseApprovedClean || s.ApprovedAt == nil {
		return ErrReviewRequired
	}
	s.Phase = PhaseActiveComparison
	s.Revision++
	return nil
}

type Deviation struct {
	Window  Window             `json:"window"`
	Ratios  map[string]float64 `json:"ratios"`
	Maximum float64            `json:"maximum"`
}

func (s State) Compare(window Window, recent Aggregate) (Deviation, error) {
	if s.Phase != PhaseActiveComparison || !validWindow(window) || validateAggregate(recent) != nil || recent.Samples == 0 {
		return Deviation{}, ErrReviewRequired
	}
	base := s.Baselines[window]
	if base.Samples == 0 {
		return Deviation{}, errors.New("baseline window unavailable")
	}
	out := Deviation{Window: window, Ratios: make(map[string]float64)}
	for feature, count := range recent.FeatureCounts {
		recentRate := float64(count) / float64(recent.Samples)
		baseRate := float64(base.FeatureCounts[feature]) / float64(base.Samples)
		ratio := recentRate / math.Max(baseRate, 1/float64(base.Samples))
		out.Ratios[feature] = ratio
		if ratio > out.Maximum {
			out.Maximum = ratio
		}
	}
	return out, nil
}

func validateAggregate(a Aggregate) error {
	if a.Samples == 0 || len(a.FeatureCounts) > 256 {
		return errors.New("invalid aggregate")
	}
	for k, v := range a.FeatureCounts {
		if len(k) < 1 || len(k) > 64 || v > a.Samples {
			return errors.New("invalid aggregate feature")
		}
		for _, r := range k {
			if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				return errors.New("invalid aggregate feature")
			}
		}
	}
	return nil
}

func validWindow(w Window) bool { return w == WindowWeekly || w == WindowMonthly || w == WindowYearly }

// WindowBounds returns the last completed UTC calendar window. Using completed
// windows makes results independent of local timezone, DST and invocation time.
func WindowBounds(w Window, asOf time.Time) (time.Time, time.Time, error) {
	if !validWindow(w) || asOf.IsZero() {
		return time.Time{}, time.Time{}, errors.New("invalid comparison window")
	}
	u := asOf.UTC()
	var end time.Time
	switch w {
	case WindowWeekly:
		daysSinceMonday := (int(u.Weekday()) + 6) % 7
		end = time.Date(u.Year(), u.Month(), u.Day()-daysSinceMonday, 0, 0, 0, 0, time.UTC)
		return end.AddDate(0, 0, -7), end, nil
	case WindowMonthly:
		end = time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
		return end.AddDate(0, -1, 0), end, nil
	case WindowYearly:
		end = time.Date(u.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		return end.AddDate(-1, 0, 0), end, nil
	}
	panic("unreachable")
}

func merge(a, b Aggregate) Aggregate {
	out := Aggregate{Samples: a.Samples + b.Samples, FeatureCounts: make(map[string]uint64, len(a.FeatureCounts)+len(b.FeatureCounts))}
	for k, v := range a.FeatureCounts {
		out.FeatureCounts[k] = v
	}
	for k, v := range b.FeatureCounts {
		out.FeatureCounts[k] += v
	}
	return out
}
