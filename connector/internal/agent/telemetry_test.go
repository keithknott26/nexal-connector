package agent

import "testing"

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
