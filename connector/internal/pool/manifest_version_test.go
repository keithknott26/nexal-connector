package pool

import (
	"errors"
	"os"
	"testing"
)

func TestManifestVersionRejectsInvalidInputsAndMissingRecords(t *testing.T) {
	s := testStore(t, 1<<20, nil)
	if _, err := s.ManifestVersion("project.report", 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("version 0 accepted: %v", err)
	}
	if _, err := s.ManifestVersion("../escape", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe key accepted: %v", err)
	}
	if _, err := s.ManifestVersion("project.report", 1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing version should not exist: %v", err)
	}
}

func TestManifestVersionAfterCloseReturnsErrClosed(t *testing.T) {
	s := testStore(t, 1<<20, nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManifestVersion("project.report", 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed store served a manifest: %v", err)
	}
}
