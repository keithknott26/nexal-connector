package runpod

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

const testKey = "rpa_SECRETSECRETSECRET1234"

type call struct {
	Method, Path, Auth string
	Body               map[string]any
}

// fake is a RunPod stand-in that records every request.
type fake struct {
	mu      sync.Mutex
	calls   []call
	failEP  int  // status to return for POST /endpoints (0: succeed)
	missing bool // GET/DELETE of an endpoint or template answers 404
	srv     *httptest.Server
}

func newFake(t *testing.T) (*fake, *Client) {
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
		f.mu.Lock()
		f.calls = append(f.calls, call{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), body})
		failEP, missing := f.failEP, f.missing
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/templates":
			w.Write([]byte(`{"id":"tpl123"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/endpoints":
			if failEP != 0 {
				w.WriteHeader(failEP)
				w.Write([]byte(`{"error":"no capacity for the chosen GPUs ` + testKey + `"}`))
				return
			}
			w.Write([]byte(`{"id":"ep456"}`))
		case missing:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not found"}`))
		case r.Method == http.MethodGet:
			w.Write([]byte(`{"id":"ep456"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f, &Client{Base: f.srv.URL, Key: testKey}
}

func standard() Spec {
	p, _ := PresetByID("standard")
	return Spec{Model: p.Model, Quantization: p.Quantization, GPUTypes: p.GPUTypes, MaxModelLen: p.MaxModelLen, DiskGB: p.DiskGB}
}

func TestProvisionCreatesTemplateThenScaleToZeroEndpoint(t *testing.T) {
	f, c := newFake(t)
	res, err := c.Provision(context.Background(), standard())
	if err != nil {
		t.Fatal(err)
	}
	if res.EndpointID != "ep456" || res.TemplateID != "tpl123" || res.Model != "Qwen/Qwen3-32B-AWQ" ||
		res.BaseURL != "https://api.runpod.ai/v2/ep456/openai/v1" {
		t.Fatalf("unexpected result %+v", res)
	}
	if len(f.calls) != 2 || f.calls[0].Path != "/templates" || f.calls[1].Path != "/endpoints" {
		t.Fatalf("want template then endpoint, got %+v", f.calls)
	}
	for _, c := range f.calls {
		if c.Auth != "Bearer "+testKey {
			t.Fatalf("missing bearer auth on %s", c.Path)
		}
	}
	tpl := f.calls[0].Body
	if tpl["isServerless"] != true || tpl["imageName"] != WorkerImage || !strings.HasPrefix(tpl["name"].(string), "nexal-overflow-") {
		t.Fatalf("bad template %+v", tpl)
	}
	env := tpl["env"].(map[string]any)
	if env["MODEL_NAME"] != "Qwen/Qwen3-32B-AWQ" || env["QUANTIZATION"] != "awq" || env["MAX_MODEL_LEN"] != "16384" {
		t.Fatalf("bad worker env %+v", env)
	}
	ep := f.calls[1].Body
	if ep["templateId"] != "tpl123" || ep["workersMin"] != float64(0) || ep["workersMax"] != float64(1) ||
		ep["computeType"] != "GPU" || ep["flashboot"] != true || ep["minCudaVersion"] != DefaultMinCUDA {
		t.Fatalf("endpoint must scale to zero with one worker at most: %+v", ep)
	}
	if g := ep["gpuTypeIds"].([]any); len(g) != 5 || g[0] != "NVIDIA A40" {
		t.Fatalf("GPU priority order lost: %+v", g)
	}
}

func TestFailedEndpointDeletesItsTemplateAndHidesTheKey(t *testing.T) {
	f, c := newFake(t)
	f.failEP = http.StatusBadRequest
	_, err := c.Provision(context.Background(), standard())
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("the API key leaked into the error: %v", err)
	}
	last := f.calls[len(f.calls)-1]
	if last.Method != http.MethodDelete || last.Path != "/templates/tpl123" {
		t.Fatalf("the half-made template must be deleted, got %+v", f.calls)
	}
}

func TestRejectedKeyIsExplained(t *testing.T) {
	f, c := newFake(t)
	f.failEP = http.StatusUnauthorized
	_, err := c.Provision(context.Background(), standard())
	if err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Fatalf("want a key explanation, got %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatal("key leaked")
	}
}

func TestNoKeyMeansNoRequest(t *testing.T) {
	f, _ := newFake(t)
	c := &Client{Base: f.srv.URL}
	if _, err := c.Provision(context.Background(), standard()); err == nil || len(f.calls) != 0 {
		t.Fatalf("a missing key must fail before any request: %v %+v", err, f.calls)
	}
}

func TestSpecIsValidatedBeforeAnythingIsCreated(t *testing.T) {
	f, c := newFake(t)
	bad := []Spec{
		{Model: "no-slash", GPUTypes: []string{"NVIDIA A40"}},
		{Model: "a/b", GPUTypes: nil},
		{Model: "a/b", GPUTypes: []string{"NVIDIA A40\nx"}},
		{Model: "a/b", GPUTypes: []string{"NVIDIA A40"}, MaxWorkers: 11},
		{Model: "a/b", GPUTypes: []string{"NVIDIA A40"}, Image: "evil image"},
		{Model: "a/b", GPUTypes: []string{"NVIDIA A40"}, MinCUDA: "abc"},
	}
	for i, s := range bad {
		if _, err := c.Provision(context.Background(), s); err == nil {
			t.Fatalf("case %d should be rejected", i)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("invalid specs must not reach RunPod: %+v", f.calls)
	}
}

func TestExistsAndRemove(t *testing.T) {
	f, c := newFake(t)
	ok, err := c.Exists(context.Background(), "ep456")
	if err != nil || !ok {
		t.Fatalf("want it to exist: %v %v", ok, err)
	}
	if err := c.Remove(context.Background(), "ep456", "tpl123"); err != nil {
		t.Fatal(err)
	}
	got := f.calls[len(f.calls)-2:]
	if got[0].Path != "/endpoints/ep456" || got[1].Path != "/templates/tpl123" || got[0].Method != http.MethodDelete {
		t.Fatalf("endpoint must go before its template: %+v", got)
	}
	f.missing = true
	if ok, err := c.Exists(context.Background(), "ep456"); err != nil || ok {
		t.Fatalf("a 404 means gone, not an error: %v %v", ok, err)
	}
	if err := c.Remove(context.Background(), "ep456", "tpl123"); err != nil {
		t.Fatalf("removing what is already gone must succeed: %v", err)
	}
}

func TestIDsCannotEscapeThePath(t *testing.T) {
	f, c := newFake(t)
	for _, id := range []string{"../templates/x", "a/b", "", "a b", strings.Repeat("a", 65)} {
		if _, err := c.Exists(context.Background(), id); err == nil {
			t.Fatalf("%q should be refused", id)
		}
	}
	if err := c.Remove(context.Background(), "ep/../x", ""); err == nil {
		t.Fatal("remove must refuse a bad id")
	}
	if len(f.calls) != 0 {
		t.Fatalf("nothing should be sent: %+v", f.calls)
	}
}

func TestPresetsAreComplete(t *testing.T) {
	for _, p := range Presets {
		s := Spec{Model: p.Model, Quantization: p.Quantization, GPUTypes: p.GPUTypes, MaxModelLen: p.MaxModelLen, DiskGB: p.DiskGB}
		if err := s.validate(); err != nil {
			t.Fatalf("preset %s: %v", p.ID, err)
		}
	}
	if _, ok := PresetByID("nope"); ok {
		t.Fatal("unknown preset")
	}
}

func TestErrorMessageAndErrorText(t *testing.T) {
	if got := errorMessage([]byte(`{"message":"  two \n spaces "}`)); got != "two spaces" {
		t.Fatalf("message fallback and whitespace collapse: %q", got)
	}
	if got := errorMessage([]byte(`not json`)); got != "" {
		t.Fatalf("non-JSON body should give no message, got %q", got)
	}
	long := errorMessage([]byte(`{"error":"` + strings.Repeat("é", 400) + `"}`))
	if len(long) > 300 || !utf8.ValidString(long) {
		t.Fatalf("truncation must stay within 300 bytes and valid UTF-8: len=%d valid=%v", len(long), utf8.ValidString(long))
	}
	if got := (&Error{Status: 429}).Error(); !strings.Contains(got, "rate limiting") {
		t.Fatalf("429 text: %q", got)
	}
	if got := (&Error{Status: 500}).Error(); got != "RunPod returned 500" {
		t.Fatalf("bare status text: %q", got)
	}
	if got := (&Error{Status: 400, Message: "bad"}).Error(); got != "RunPod returned 400: bad" {
		t.Fatalf("status with message: %q", got)
	}
}

func TestValidIDAndValidModelAcceptOnlySafeShapes(t *testing.T) {
	for _, s := range []string{"abc123", "a_b-C", strings.Repeat("a", 64)} {
		if !ValidID(s) {
			t.Errorf("ValidID(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "a/b", "../x", "a b", "a?b", strings.Repeat("a", 65)} {
		if ValidID(s) {
			t.Errorf("ValidID(%q) = true, want false", s)
		}
	}
	for _, s := range []string{"Qwen/Qwen3-14B-AWQ", "a/b", "org.x/model_1.5"} {
		if !ValidModel(s) {
			t.Errorf("ValidModel(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "noslash", "a/b/c", "/b", "a/", "-a/b", "a/-b", "a b/c", "a/b;rm"} {
		if ValidModel(s) {
			t.Errorf("ValidModel(%q) = true, want false", s)
		}
	}
}

func TestBaseURLForAndIsNotFound(t *testing.T) {
	if got, want := BaseURLFor("ep1"), "https://api.runpod.ai/v2/ep1/openai/v1"; got != want {
		t.Errorf("BaseURLFor = %q, want %q", got, want)
	}
	if !IsNotFound(&Error{Status: 404}) {
		t.Error("a 404 Error must be NotFound")
	}
	if !IsNotFound(fmt.Errorf("wrapped: %w", &Error{Status: 404})) {
		t.Error("a wrapped 404 Error must be NotFound")
	}
	if IsNotFound(&Error{Status: 500}) || IsNotFound(errors.New("plain")) || IsNotFound(nil) {
		t.Error("non-404 errors must not be NotFound")
	}
}

func TestSuffixIsEightHexCharsAndVaries(t *testing.T) {
	a, b := suffix(), suffix()
	if len(a) != 8 || len(b) != 8 {
		t.Fatalf("suffix lengths = %d, %d, want 8", len(a), len(b))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Errorf("suffix %q is not hex: %v", a, err)
	}
	if a == b {
		t.Errorf("two suffixes were identical: %q", a)
	}
}


func TestProvisionPassesAHuggingFaceTokenToTheWorker(t *testing.T) {
	f, c := newFake(t)
	spec := standard()
	spec.HFToken = "hf_testtoken"
	if _, err := c.Provision(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	env := f.calls[0].Body["env"].(map[string]any)
	if env["HF_TOKEN"] != "hf_testtoken" {
		t.Fatalf("worker env %+v", env)
	}
	f2, c2 := newFake(t)
	if _, err := c2.Provision(context.Background(), standard()); err != nil {
		t.Fatal(err)
	}
	if _, has := f2.calls[0].Body["env"].(map[string]any)["HF_TOKEN"]; has {
		t.Fatal("no token was given, so none must be sent")
	}
}
