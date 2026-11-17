package inference

import (
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestEscapeRepoAcceptsOwnerNameAndRejectsTheRest(t *testing.T) {
	got, err := escapeRepo("mlx-community/Qwen3-4B-4bit")
	if err != nil || got != "mlx-community/Qwen3-4B-4bit" {
		t.Fatalf("escapeRepo = %q, %v", got, err)
	}
	for _, bad := range []string{"", "noslash", "a/b/c", "../etc/passwd", "owner/..", "owner/na me", "owner/name?x=1", "/name", "owner/"} {
		_, err := escapeRepo(bad)
		wantCode(t, err, "invalid_arguments")
	}
}

func TestScrubURLErrorDropsTheURLButKeepsTheCause(t *testing.T) {
	cause := errors.New("connection refused")
	ue := &url.Error{Op: "Get", URL: "https://huggingface.co/secret?token=abc", Err: cause}
	if got := scrubURLError(ue); got != cause {
		t.Fatalf("scrubURLError(url.Error) = %v, want the bare cause", got)
	}
	plain := errors.New("plain")
	if got := scrubURLError(plain); got != plain {
		t.Fatalf("scrubURLError(plain) = %v, want it unchanged", got)
	}
}

func TestCancelledErrorIsResumable(t *testing.T) {
	e := cancelled()
	if e.Code != "cancelled" || e.Fix == "" {
		t.Fatalf("cancelled() = %+v", e)
	}
}

func TestRetryWaitDefaultsAndOverride(t *testing.T) {
	if got := (&HFClient{}).retryWait(); got != 3*time.Second {
		t.Fatalf("default retryWait = %v", got)
	}
	if got := (&HFClient{retryBase: time.Millisecond}).retryWait(); got != time.Millisecond {
		t.Fatalf("override retryWait = %v", got)
	}
}

func newURLTestClient() *HFClient {
	return &HFClient{
		base:  &url.URL{Scheme: "https", Host: "huggingface.co"},
		allow: HFHostAllowed,
	}
}

func TestCheckURLRefusesInsecureCredentialedAndForeignHosts(t *testing.T) {
	c := newURLTestClient()
	if e := c.checkURL(&url.URL{Scheme: "https", Host: "huggingface.co", Path: "/x"}); e != nil {
		t.Fatalf("allowed host refused: %v", e)
	}
	cases := []struct {
		name string
		u    url.URL
		code string
	}{
		{"http", url.URL{Scheme: "http", Host: "huggingface.co"}, "insecure_url"},
		{"credentials", url.URL{Scheme: "https", Host: "huggingface.co", User: url.UserPassword("u", "p")}, "insecure_url"},
		{"foreign host", url.URL{Scheme: "https", Host: "evil.example"}, "host_not_allowed"},
	}
	for _, tc := range cases {
		u := tc.u
		e := c.checkURL(&u)
		if e == nil || e.Code != tc.code {
			t.Errorf("%s: checkURL = %v, want code %q", tc.name, e, tc.code)
		}
	}
}

func TestAPIURLBuildsModelPathAndDropsQuery(t *testing.T) {
	c := newURLTestClient()
	c.base.RawQuery = "token=abc"
	u, err := c.apiURL("mlx-community/Qwen3-4B-4bit", "/tree/main")
	if err != nil {
		t.Fatal(err)
	}
	if got := u.String(); got != "https://huggingface.co/api/models/mlx-community/Qwen3-4B-4bit/tree/main" {
		t.Fatalf("apiURL = %q", got)
	}
	if c.base.RawQuery != "token=abc" {
		t.Fatal("apiURL mutated the client's base URL")
	}
	_, err = c.apiURL("../etc/passwd", "")
	wantCode(t, err, "invalid_arguments")
}

func TestResolveURLRequiresPinnedRevisionAndSafeName(t *testing.T) {
	c := newURLTestClient()
	rev := "0123456789abcdef0123456789abcdef01234567"
	u, err := c.resolveURL("owner/name", rev, "model.safetensors")
	if err != nil {
		t.Fatal(err)
	}
	if got := u.String(); got != "https://huggingface.co/owner/name/resolve/"+rev+"/model.safetensors" {
		t.Fatalf("resolveURL = %q", got)
	}
	_, err = c.resolveURL("owner/name", "main", "model.safetensors")
	wantCode(t, err, "mutable_revision")
	_, err = c.resolveURL("owner/name", rev, "../escape")
	wantCode(t, err, "bad_file_name")
	_, err = c.resolveURL("bad", rev, "f")
	wantCode(t, err, "invalid_arguments")
}
