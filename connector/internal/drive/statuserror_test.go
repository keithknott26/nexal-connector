package drive

import "testing"

func TestStatusErrorMessageFormats(t *testing.T) {
	withMsg := (&StatusError{Status: 413, Code: "quota_exceeded", Message: "over quota"}).Error()
	if want := "coordinator refused the request (HTTP 413, quota_exceeded): over quota"; withMsg != want {
		t.Fatalf("with message = %q, want %q", withMsg, want)
	}
	// Without a message the code is deliberately omitted: it is not shown
	// unless the coordinator also supplied human-readable text.
	bare := (&StatusError{Status: 502, Code: "ignored"}).Error()
	if want := "coordinator refused the request (HTTP 502)"; bare != want {
		t.Fatalf("without message = %q, want %q", bare, want)
	}
}
