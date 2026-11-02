package presence

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/sysinfo"
)

func TestCleanInfoStripsControlsAndBoundsLength(t *testing.T) {
	in := sysinfo.Info{
		Name:     "mac\x00book\n\t\x7f-1",
		OS:       strings.Repeat("a", maxInfoText+50),
		Location: "Berlin",
	}
	out := cleanInfo(in)
	if out.Name != "macbook-1" {
		t.Fatalf("control characters not stripped: %q", out.Name)
	}
	if len(out.OS) != maxInfoText {
		t.Fatalf("OS length = %d, want %d", len(out.OS), maxInfoText)
	}
	if out.Location != "Berlin" {
		t.Fatalf("clean field altered: %q", out.Location)
	}
}

func TestShouldReportInfo(t *testing.T) {
	base := sysinfo.Info{Name: "a"}
	if !shouldReportInfo(base, base, false, 0) {
		t.Fatal("first report must always be sent")
	}
	if shouldReportInfo(base, base, true, time.Second) {
		t.Fatal("unchanged info inside the refresh interval must not be resent")
	}
	if !shouldReportInfo(base, base, true, InfoRefreshInterval) {
		t.Fatal("refresh interval elapsed must resend")
	}
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleep returned %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sleep = %v, want context.Canceled", err)
	}
}
