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
	scanner := Scanner{Directory: dir}
	checks, err := canary.ExerciseChecks(context.Background(), scanner)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, check := range checks {
		got[check.Name] = check.Status
	}
	if got["canary_integrity"] != "detected" || got["canary_file_read"] != "missed" || got["synthetic_credential_use"] != "unavailable" || got["scanner_fixture"] != "unavailable" {
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
	scanner := Scanner{Directory: dir}
	firstID := ""
	err := canary.RunExercises(context.Background(), scanner, func(_ context.Context, event Event) error { firstID = event.EventID; return errors.New("offline") })
	if err == nil {
		t.Fatal("delivery failure lost")
	}
	if _, err = os.Stat(filepath.Join(dir, "security-exercises", "pending.json")); err != nil {
		t.Fatal("missing retry journal")
	}
	count := 0
	err = canary.RunExercises(context.Background(), scanner, func(_ context.Context, event Event) error {
		if count == 0 && event.EventID != firstID {
			t.Fatal("changed retry identity")
		}
		count++
		return nil
	})
	if err != nil || count != 4 {
		t.Fatalf("retry failed: %v count%d", err, count)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if canary.RunExercises(ctx, scanner, func(context.Context, Event) error { t.Fatal("ran disconnected"); return nil }) == nil {
		t.Fatal("cancellation ignored")
	}
}
