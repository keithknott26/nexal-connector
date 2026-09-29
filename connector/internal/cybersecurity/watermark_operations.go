package cybersecurity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type WatermarkOperation struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	ExpiresAt string `json:"expiresAt"`
}
type WatermarkCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}
type WatermarkResult struct {
	ID     string           `json:"id"`
	Kind   string           `json:"kind"`
	Checks []WatermarkCheck `json:"checks"`
}
type watermarkRotation struct {
	ID       string `json:"id"`
	File     string `json:"file"`
	Baseline string `json:"baseline"`
}

var operationID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// finishRotation is recoverable after the atomic replacement but before state
// persistence. Only the fixed owned decoy is replaced; symlink targets are never opened.
func finishRotation(root *os.Root, s *CanaryState) error {
	r := s.Rotation
	if r == nil {
		return nil
	}
	if !operationID.MatchString(r.ID) || r.File != "replacement-"+r.ID || !digestID.MatchString(r.Baseline) {
		return errors.New("invalid rotation journal")
	}
	if _, err := root.Lstat(r.File); err == nil {
		if err := root.Rename(r.File, canaryName); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if canaryFingerprint(root) != r.Baseline {
		return errors.New("replacement verification failed")
	}
	s.Baseline = r.Baseline
	s.Observed = r.Baseline
	s.Status = "watching"
	if !s.Enabled {
		s.Status = "disabled"
	}
	s.LastCheckedAt = time.Now().UTC().Format(time.RFC3339)
	s.OperationReported = false
	s.OperationResult = &WatermarkResult{r.ID, "regenerate", []WatermarkCheck{{"watermark_regeneration", "passed"}}}
	s.Rotation = nil
	return saveCanary(root, s)
}

// ExecuteOperation accepts only two fixed actions from the authenticated
// coordinator. No paths, shell commands, rule bytes or credentials are accepted.
func (c Canary) ExecuteOperation(ctx context.Context, op WatermarkOperation, report func(context.Context, Event) error) error {
	expiry, err := time.Parse(time.RFC3339Nano, op.ExpiresAt)
	if err != nil || !operationID.MatchString(op.ID) || !contains(op.Kind, "regenerate", "self_test") || !expiry.After(time.Now()) || expiry.After(time.Now().Add(11*time.Minute)) {
		return errors.New("invalid or expired watermark operation")
	}
	return c.locked(func(root *os.Root, s *CanaryState) error {
		if s.Rotation != nil {
			if err := finishRotation(root, s); err != nil {
				return err
			}
		}
		if s.OperationResult != nil && s.OperationResult.ID == op.ID {
			if s.OperationResult.Kind != op.Kind {
				return errors.New("operation conflict")
			}
			return nil
		}
		result := WatermarkResult{ID: op.ID, Kind: op.Kind}
		if op.Kind == "regenerate" {
			status := "blocked"
			// Never overwrite a new change that the monitoring loop has not observed.
			// A previously delivered change may be rotated, preserving LastEvent.
			if s.Enabled && s.Pending == nil && canaryFingerprint(root) == s.Observed {
				random := make([]byte, 32)
				if _, err := rand.Read(random); err != nil {
					return err
				}
				content := []byte("neXal integrity canary — not a real credential\n" + hex.EncodeToString(random) + "\n")
				name := "replacement-" + op.ID
				f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err != nil {
					return err
				}
				_, err = f.Write(content)
				if err == nil {
					err = f.Sync()
				}
				closeErr := f.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
				s.Rotation = &watermarkRotation{op.ID, name, digest(content)}
				if err := saveCanary(root, s); err != nil {
					return err
				}
				return finishRotation(root, s)
			}
			result.Checks = []WatermarkCheck{{"watermark_regeneration", status}}
		} else {
			integrity := "not_configured"
			if s.Enabled {
				integrity = "failed"
				if s.Pending == nil && s.Rotation == nil && canaryFingerprint(root) == s.Baseline && c.testIntegrity(ctx) == nil {
					integrity = "passed"
				}
			}
			result.Checks = append(result.Checks, WatermarkCheck{"watermark_integrity", integrity})
			delivery := "failed"
			event := Event{SchemaVersion: 1, EventID: "watermark_test_" + op.ID, ObservedAt: time.Now().UTC().Format(TimeLayout), Kind: "sensor_health", Severity: "info", Detector: "nexal_watermark_self_test", DetectorVersion: "1", OriginAssessment: "unknown", EvidenceRef: "test_" + op.ID}
			if report != nil && report(ctx, event) == nil {
				delivery = "passed"
			}
			result.Checks = append(result.Checks, WatermarkCheck{"report_delivery", delivery}, WatermarkCheck{"read_sensor", "unavailable"}, WatermarkCheck{"copy_sensor", "unavailable"}, WatermarkCheck{"outbound_sensor", "unavailable"})
		}
		s.OperationReported = false
		s.OperationResult = &result
		return saveCanary(root, s)
	})
}
func (c Canary) testIntegrity(ctx context.Context) error {
	temp, err := os.MkdirTemp(c.Directory, "watermark-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	test := Canary{Directory: temp}
	if err := test.Configure(true); err != nil {
		return err
	}
	path := filepath.Join(temp, "security-canary", canaryName)
	if err := os.WriteFile(path, []byte("synthetic integrity test"), 0600); err != nil {
		return err
	}
	detected := false
	if err := test.Tick(ctx, time.Now(), func(_ context.Context, e Event) error { detected = e.Detector == "nexal_canary_modified"; return nil }); err != nil {
		return err
	}
	if !detected {
		return errors.New("integrity self-test failed")
	}
	return nil
}
func (c Canary) MarkOperationReported(id string) error {
	return c.locked(func(root *os.Root, s *CanaryState) error {
		if s.OperationResult != nil && s.OperationResult.ID == id {
			s.OperationReported = true
			return saveCanary(root, s)
		}
		return nil
	})
}

func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
