package sysinfo

import (
	"context"
	"errors"
	"runtime"
	"testing"
)

// On non-macOS hosts the collector must not shell out to macOS-only tools and
// must fall back to the Go runtime's view of the machine.
func TestReadStaticAndThermalOffDarwinNeverRunCommands(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("covers the non-darwin fallback only")
	}
	c := &Collector{Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("no external command may run off darwin")
		return nil, errors.New("unreachable")
	}}
	info := c.readStatic(context.Background())
	if info.OS != runtime.GOOS || info.Cores != runtime.NumCPU() {
		t.Fatalf("fallback static info wrong: %+v", info)
	}
	if got := c.thermal(context.Background()); got != "" {
		t.Fatalf("thermal off darwin should be empty, got %q", got)
	}
}
