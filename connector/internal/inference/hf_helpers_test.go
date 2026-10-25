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
