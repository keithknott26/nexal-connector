package agent

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestSafeIORegistryParsing(t *testing.T) {
	good := []byte("+ -o IOHIDSystem\n    \"HIDIdleTime\" = 600000000000\n")
	seconds, err := ParseIdle(good)
	if err != nil || seconds != 600 {
		t.Fatalf("%d %v", seconds, err)
	}
	for _, bad := range []string{
		`"HIDIdleTime" = -1`, `"HIDIdleTime" = 1e9`, `"HIDIdleTime" = $(evil)`,
		`"NotHIDIdleTime" = 1000000000`, `"HIDIdleTime" = 18446744073709551616`,
		"\"HIDIdleTime\" = 1\n\"HIDIdleTime\" = 2\n", "",
	} {
		if _, err := ParseIdle([]byte(bad)); err == nil {
			t.Errorf("unsafe idle value accepted: %s", bad)
		}
	}
}
func TestConservativeMemoryParsing(t *testing.T) {
	b := []byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 100.\nPages speculative: 50.\nPages active: 999999.\n")
	got, err := ParseFreeMemory(b)
	if err != nil || got != 150*16384 {
		t.Fatalf("%d %v", got, err)
	}
	if _, err := ParseFreeMemory([]byte("page size of 16384 bytes\nPages free: 9999999999999999.\nPages speculative: 1.")); err == nil {
		t.Fatal("overflow memory accepted")
	}
	if _, err := ParseFreeMemory([]byte("Pages free: 1.")); err == nil {
		t.Fatal("missing telemetry accepted")
	}
}

func TestBoundedBufferRejectsOverflow(t *testing.T) {
	b := &boundedBuffer{max: 4}
	if n, err := b.Write([]byte("abcd")); err != nil || n != 4 {
		t.Fatalf("write at limit: %d %v", n, err)
	}
	if n, err := b.Write([]byte("e")); err == nil || n != 0 {
		t.Fatalf("write past limit: %d %v", n, err)
	}
	if got := b.String(); got != "abcd" {
		t.Fatalf("buffer changed after rejected write: %q", got)
	}
}

func TestDiagnosticReturnsOutputAndScrubsFailures(t *testing.T) {
	out, err := diagnostic(context.Background(), "/bin/echo", "hello")
	if err != nil || strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	// A missing binary and a non-zero exit both collapse to the same generic
	// error so no path or stderr detail leaks to callers.
	for _, path := range []string{"/nonexistent/nexal-diagnostic", "/bin/false"} {
		out, err := diagnostic(context.Background(), path)
		if err == nil || err.Error() != "telemetry unavailable" || out != nil {
			t.Errorf("%s: out=%q err=%v", path, out, err)
		}
	}
}

func TestMacProbeOffDarwinAssumesOwnerActive(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("probe shells out to macOS tools on darwin")
	}
	got := MacProbe(300)(context.Background())
	if !got.OwnerActive || got.Known || got.TotalMemoryBytes != 0 || got.AvailableMemoryBytes != 0 {
		t.Fatalf("off-darwin probe must report an unknown, active owner: %+v", got)
	}
}

func TestParseFreeMemoryRejectsOversizedInput(t *testing.T) {
	if _, err := ParseFreeMemory(make([]byte, 256<<10+1)); err == nil {
		t.Fatal("oversized telemetry accepted")
	}
}
