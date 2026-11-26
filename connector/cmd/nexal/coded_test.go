package main

import (
	"errors"
	"testing"

	"nexal/connector/internal/inference"
)

func TestCodedPassesNilThroughAndMapsErrorCodes(t *testing.T) {
	if coded(nil) != nil {
		t.Fatal("coded(nil) must stay nil so success paths are not turned into failures")
	}
	typed := &inference.Error{Code: "model_missing", Message: "no such model"}
	if got := errorCode(coded(typed)); got != "model_missing" {
		t.Fatalf("typed inference error code = %q, want model_missing", got)
	}
	if got := errorCode(coded(errors.New("disk full"))); got != "io_error" {
		t.Fatalf("plain error code = %q, want io_error", got)
	}
	if err := coded(errors.New("disk full")); err.Error() != "disk full" {
		t.Fatalf("message = %q, want it preserved", err.Error())
	}
}
