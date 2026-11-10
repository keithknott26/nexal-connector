package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pause and resume must round-trip through the local API, and cancel must be
// accepted as a parameterless command that returns the current snapshot.
func TestLocalAPIPauseResumeCancelRoundTrip(t *testing.T) {
	a, _ := testAgent(t)
	token := strings.Repeat("x", 64)
	h, err := a.Handler(token)
	if err != nil {
		t.Fatal(err)
	}
	post := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8788"+path, strings.NewReader("{}"))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post("/v1/pause"); w.Code != http.StatusOK || !a.Snapshot().Paused {
		t.Fatalf("pause: got %d paused=%v", w.Code, a.Snapshot().Paused)
	}
	if w := post("/v1/resume"); w.Code != http.StatusOK || a.Snapshot().Paused {
		t.Fatalf("resume: got %d paused=%v", w.Code, a.Snapshot().Paused)
	}
	if w := post("/v1/cancel"); w.Code != http.StatusOK {
		t.Fatalf("cancel: got %d %s", w.Code, w.Body.String())
	}
}
