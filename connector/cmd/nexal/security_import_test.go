package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecurityCaptureValidationAndStableRetry(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	row := `{"event_type":"alert","timestamp":"` + now.Format(time.RFC3339Nano) + `","alert":{"signature_id":7,"severity":1},"src_ip":"192.168.1.2","payload":"secret"}`
	if err := os.WriteFile(path, []byte(row+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		events, err := readSecurityCapture(f, "opaque_capture_7", "rules_1", now)
		if err != nil || len(events) != 1 {
			t.Fatal(events, err)
		}
		return events[0].EventID
	}
	if read() != read() {
		t.Fatal("retry changed event ID")
	}
	if err := os.WriteFile(path, []byte(row+"\nnot-json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(path)
	defer f.Close()
	events, err := readSecurityCapture(f, "opaque_capture_7", "rules_1", now)
	if err == nil || events != nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("invalid batch must not upload or leak evidence", events, err)
	}
}
