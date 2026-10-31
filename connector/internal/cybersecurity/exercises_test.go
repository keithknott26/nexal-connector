package cybersecurity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExercisesRealIntegrityAndHonestCoverage(t *testing.T) {
	dir := t.TempDir()
	canary := Canary{Directory: dir}
	checks, err := canary.ExerciseChecks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, check := range checks {
		got[check.Name] = check.Status
	}
	if got["canary_integrity"] != "detected" || got["canary_file_read"] != "missed" || got["synthetic_credential_use"] != "unavailable" || len(got) != 3 {
		t.Fatalf("dishonest coverage: %+v", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.IsDir() && (len(entry.Name()) > 14 && entry.Name()[:14] == "read-exercise-") {
			t.Fatal("exercise artifact leaked")
		}
	}
}
func TestExerciseJournalRetryAndCancellation(t *testing.T) {
	dir := t.TempDir()
	canary := Canary{Directory: dir}
	firstID := ""
	err := canary.RunExercises(context.Background(), func(_ context.Context, event Event) error { firstID = event.EventID; return errors.New("offline") })
	if err == nil {
		t.Fatal("delivery failure lost")
	}
	if _, err = os.Stat(filepath.Join(dir, "security-exercises", "pending.json")); err != nil {
		t.Fatal("missing retry journal")
	}
	count := 0
	err = canary.RunExercises(context.Background(), func(_ context.Context, event Event) error {
		if count == 0 && event.EventID != firstID {
			t.Fatal("changed retry identity")
		}
		count++
		return nil
	})
	if err != nil || count != 3 {
		t.Fatalf("retry failed: %v count%d", err, count)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if canary.RunExercises(ctx, func(context.Context, Event) error { t.Fatal("ran disconnected"); return nil }) == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestExerciseJournalRejectsCorruptOrOversizedState(t *testing.T) {
	cases := map[string]string{
		"corrupt":   "{not json",
		"oversized": "[" + strings.Repeat(" ", 16384) + "]",
		"too many":  "[{},{},{},{},{},{},{},{},{}]",
	}
	for name, journal := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			directory := filepath.Join(dir, "security-exercises")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "pending.json"), []byte(journal), 0600); err != nil {
				t.Fatal(err)
			}
			err := Canary{Directory: dir}.RunExercises(context.Background(), func(context.Context, Event) error { t.Fatal("delivered from invalid journal"); return nil })
			if err == nil || err.Error() != "invalid exercise journal" {
				t.Fatalf("invalid journal accepted: %v", err)
			}
		})
	}
}

func TestExerciseJournalDropsStaleInvalidEventsButKeepsFreshOnes(t *testing.T) {
	dir := t.TempDir()
	canary := Canary{Directory: dir}
	if err := canary.RunExercises(context.Background(), func(context.Context, Event) error { return errors.New("offline") }); err == nil {
		t.Fatal("delivery failure lost")
	}
	path := filepath.Join(dir, "security-exercises", "pending.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	if err = json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	// Invalidate the first event (bad schema version) while it is still fresh.
	events[0].SchemaVersion = 99
	raw, _ = json.Marshal(events)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if canary.RunExercises(context.Background(), func(context.Context, Event) error { return nil }) == nil {
		t.Fatal("invalid fresh event accepted")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("fresh invalid journal must be kept for inspection")
	}
	// Age it past seven days: the journal is discarded instead of wedging forever.
	events[0].ObservedAt = time.Now().Add(-8 * 24 * time.Hour).UTC().Format(TimeLayout)
	raw, _ = json.Marshal(events)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = canary.RunExercises(context.Background(), func(context.Context, Event) error { return nil }); err != nil {
		t.Fatalf("stale journal not discarded: %v", err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale journal still present")
	}
}
