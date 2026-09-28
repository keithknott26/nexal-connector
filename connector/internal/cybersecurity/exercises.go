package cybersecurity

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"time"
)

// ExerciseChecks uses temporary owned artifacts and actual bundled detectors.
// It never touches user files or attempts real credential access.
func (c Canary) ExerciseChecks(ctx context.Context, scanner Scanner) ([]WatermarkCheck, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	integrity := "missed"
	if c.testIntegrity(ctx) == nil {
		integrity = "detected"
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	checks := []WatermarkCheck{{"canary_integrity", integrity}}
	for _, check := range scanner.ConfigurationSelfTest(ctx) {
		if check.Name == "scanner_fixture" {
			outcome := "unavailable"
			if check.Status == "passed" {
				outcome = "detected"
			}
			if check.Status == "failed" {
				outcome = "missed"
			}
			checks = append(checks, WatermarkCheck{"scanner_fixture", outcome})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	temp, err := os.MkdirTemp(c.Directory, "read-exercise-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	probe := Canary{Directory: temp}
	if err = probe.Configure(true); err != nil {
		return nil, err
	}
	// Read the fake credential planted by Configure. The integrity watcher does
	// not detect reads; report this real coverage gap instead of claiming a pass.
	raw, err := os.ReadFile(filepath.Join(temp, "security-canary", canaryName))
	if err != nil || len(raw) == 0 {
		return nil, errors.New("exercise artifact unavailable")
	}
	detected := false
	if err = probe.Tick(ctx, time.Now(), func(_ context.Context, e Event) error { detected = true; return nil }); err != nil {
		return nil, err
	}
	outcome := "missed"
	if detected {
		outcome = "detected"
	}
	checks = append(checks, WatermarkCheck{"canary_file_read", outcome}, WatermarkCheck{"synthetic_credential_use", "unavailable"})
	return checks, ctx.Err()
}

// RunExercises journals metadata before delivery, retaining event IDs for retry.
// Artifact bytes and the fake credential never leave this host.
func (c Canary) RunExercises(ctx context.Context, scanner Scanner, report func(context.Context, Event) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory := filepath.Join(c.Directory, "security-exercises")
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid exercise directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	var events []Event
	data, err := root.ReadFile("pending.json")
	if err == nil {
		if len(data) > 16384 || json.Unmarshal(data, &events) != nil || len(events) > 8 {
			return errors.New("invalid exercise journal")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(events) == 0 {
		checks, err := c.ExerciseChecks(ctx, scanner)
		if err != nil {
			return err
		}
		for _, check := range checks {
			id := uuid.NewString()
			events = append(events, Event{SchemaVersion: 1, EventID: "exercise_" + id, ObservedAt: time.Now().UTC().Format(TimeLayout), Kind: "sensor_health", Severity: "info", Detector: "exercise_" + check.Status + "_" + check.Name, DetectorVersion: "1", OriginAssessment: "unknown", EvidenceRef: "synthetic_" + id})
		}
		data, err = json.Marshal(events)
		if err != nil {
			return err
		}
		if err = root.WriteFile("pending.tmp", data, 0600); err != nil {
			return err
		}
		if err = root.Rename("pending.tmp", "pending.json"); err != nil {
			return err
		}
	}
	for _, event := range events {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = event.Validate(time.Now()); err != nil {
			if at, parseErr := time.Parse(TimeLayout, event.ObservedAt); parseErr == nil && time.Since(at) > 7*24*time.Hour {
				return root.Remove("pending.json")
			}
			return err
		}
		if err = report(ctx, event); err != nil {
			return err
		}
	}
	return root.Remove("pending.json")
}
