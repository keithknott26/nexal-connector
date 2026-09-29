package cybersecurity

import (
	"context"
	"github.com/google/uuid"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func validationGrant() ValidationRun {
	return ValidationRun{ID: uuid.NewString(), Nonce: uuid.NewString(), Module: "canary_integrity", ExpiresAt: time.Now().Add(time.Minute)}
}
func TestValidationBoundaries(t *testing.T) {
	for _, change := range []func(*ValidationRun){func(r *ValidationRun) { r.Module = "shell" }, func(r *ValidationRun) { r.ExpiresAt = time.Now().Add(10 * time.Minute) }, func(r *ValidationRun) { r.ID = "../../bad" }, func(r *ValidationRun) { r.Module = "network_canary"; r.TargetIP = "127.0.0.1" }} {
		r := validationGrant()
		change(&r)
		if r.Validate(time.Now()) == nil {
			t.Fatal("accepted invalid grant")
		}
	}
}
func TestValidationRealCanaryAndCleanup(t *testing.T) {
	dir := t.TempDir()
	r := validationGrant()
	calls := 0
	executed, reason, err := (Canary{Directory: dir}).ValidateLocal(context.Background(), r, func(_ context.Context, e Event) error {
		calls++
		if e.EvidenceRef != "validation_"+r.ID || e.Detector != "validation_canary_integrity" {
			t.Fatal("uncorrelated event")
		}
		return e.Validate(time.Now())
	})
	if err != nil || !executed || reason != "completed" || calls != 1 {
		t.Fatalf("%v %v %s %d", err, executed, reason, calls)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("artifact left behind")
	}
}
func TestValidationRejectsRetiredScannerModule(t *testing.T) {
	r := validationGrant()
	r.Module = "scanner_fixture"
	executed, reason, err := (Canary{Directory: t.TempDir()}).ValidateLocal(context.Background(), r, func(context.Context, Event) error { t.Fatal("fabricated evidence"); return nil })
	if err == nil || executed || reason != "execution_failed" {
		t.Fatalf("%v %v %s", err, executed, reason)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = (Canary{}).ValidateLocal(ctx, validationGrant(), nil); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestValidationHoneypotAuthorizationAndReplay(t *testing.T) {
	r := validationGrant()
	r.Module = "network_canary"
	r.TargetIP = "100.86.1.2"
	allowed := true
	reports := 0
	h, _ := validationHandler(context.Background(), r, func(context.Context) bool { return allowed }, func(context.Context, Event) error { reports++; return nil })
	request := func(token string) int {
		req := httptest.NewRequest("POST", "http://mesh/validation/"+r.ID, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if request("wrong") != 404 || reports != 0 {
		t.Fatal("unauthorized report")
	}
	allowed = false
	if request(r.Nonce) != 410 || reports != 0 {
		t.Fatal("revocation ignored")
	}
	allowed = true
	if request(r.Nonce) != 204 || reports != 1 {
		t.Fatal("missing evidence")
	}
	if request(r.Nonce) != 404 || reports != 1 {
		t.Fatal("replayed exercise")
	}
}
