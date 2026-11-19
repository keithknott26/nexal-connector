package pool

import (
	"errors"
	"strings"
	"testing"
)

func TestAppendRecordLockedRejectsOversizeRecordWithoutCharging(t *testing.T) {
	s := testStore(t, 8<<20, nil)
	rec := ManifestRecord{Manifest: Manifest{Schema: manifestSchema, Key: "project.report", Version: 1,
		Owner: strings.Repeat("o", maxManifestBytes+1)}}
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.used
	if err := s.appendRecordLocked(rec); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize record accepted: %v", err)
	}
	if s.used != before {
		t.Fatalf("rejected record charged quota: %d -> %d", before, s.used)
	}
}

func TestAppendRecordLockedReportsQuotaExhaustionWithoutCharging(t *testing.T) {
	s := testStore(t, 1<<20, nil)
	rec := ManifestRecord{Manifest: Manifest{Schema: manifestSchema, Key: "project.report", Version: 1, Owner: "o"}}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used = s.quota // no room left for even a tiny record
	if err := s.appendRecordLocked(rec); !errors.Is(err, ErrQuota) {
		t.Fatalf("expected ErrQuota, got %v", err)
	}
	if s.used != s.quota {
		t.Fatalf("failed append changed usage: %d", s.used)
	}
}
