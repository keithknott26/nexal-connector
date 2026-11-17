package drive

import (
	"errors"
	"strings"
	"testing"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout to https://secret.example/?token=abc" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestTransportErrorDistinguishesTimeoutAndNeverLeaksDetail(t *testing.T) {
	to := transportError(timeoutErr{})
	if !strings.Contains(to.Error(), "did not respond in time") {
		t.Fatalf("timeout error = %q", to)
	}
	other := transportError(errors.New("dial tcp https://secret.example: refused"))
	if !strings.Contains(other.Error(), "cannot reach the coordinator") {
		t.Fatalf("generic error = %q", other)
	}
	for _, e := range []error{to, other} {
		if strings.Contains(e.Error(), "secret") || strings.Contains(e.Error(), "token") {
			t.Fatalf("error leaked detail: %q", e)
		}
	}
}

func TestDecodeJSONRefusesBadBodiesAndToleratesUnknownFields(t *testing.T) {
	var out struct {
		A int `json:"a"`
	}
	if err := decodeJSON(strings.NewReader(`{"a":3,"extra":true}`), &out); err != nil || out.A != 3 {
		t.Fatalf("unknown field should be tolerated: err=%v out=%+v", err, out)
	}
	if err := decodeJSON(strings.NewReader(`not json`), &out); err == nil {
		t.Fatal("non-JSON body must be refused")
	}
	if err := decodeJSON(strings.NewReader(`{"a":"str"}`), &out); err == nil ||
		!strings.Contains(err.Error(), "schema") {
		t.Fatalf("wrong-typed field must report a schema error, got %v", err)
	}
}
