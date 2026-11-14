package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRekeyAndRejoinPairV2UnenrolledLeaveTheMacAloneWithoutRuntime(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, string) error
		key  string
	}{
		{"rekey", rekeyPairV2, "rekeyed"},
		{"rejoin", rejoinPairV2, "rejoined"},
	} {
		path := savedUnenrolledConfig(t)
		var err error
		out := captureStdout(t, func() { err = tc.run(context.Background(), path) })
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var got map[string]any
		if jerr := json.Unmarshal([]byte(out), &got); jerr != nil {
			t.Fatalf("%s: bad JSON %q: %v", tc.name, out, jerr)
		}
		if got[tc.key] != false || got["reason"] != "not_enrolled" {
			t.Fatalf("%s: got %v, want %s=false reason=not_enrolled", tc.name, got, tc.key)
		}
	}
}

func TestRekeyAndRejoinPairV2MissingConfigError(t *testing.T) {
	for name, run := range map[string]func(context.Context, string) error{
		"rekey": rekeyPairV2, "rejoin": rejoinPairV2,
	} {
		path := filepath.Join(t.TempDir(), "private", "config.json")
		var err error
		captureStdout(t, func() { err = run(context.Background(), path) })
		if err == nil {
			t.Fatalf("%s: expected an error for a missing configuration", name)
		}
	}
}
