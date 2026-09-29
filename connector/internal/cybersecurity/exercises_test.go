package cybersecurity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
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
