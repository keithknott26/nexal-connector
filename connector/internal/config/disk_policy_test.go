package config

import (
	"testing"

	"nexal/connector/internal/contribution"
)

// The §36.4 disk reserve goes through the same strict decoder as every other
// field: exactly-once keys, no nulls, no unknown fields, no trailing JSON. It is
// optional in PRESENCE only — a payload written before it existed must keep
// decoding, which is the UploadMode precedent in the same file.
func TestDiskReservePolicyDecodesAndKeepsOldPayloadsWorking(t *testing.T) {
	// The exact body an older client (including the shipped Swift UI) sends.
	p, err := DecodeResourcePolicy([]byte(`{` + threeField + `}`))
	if err != nil {
		t.Fatalf("a three-field payload was rejected: %v", err)
	}
	if p.MinFreeDiskBytes != 0 {
		t.Fatal("absence must decode as zero, meaning the reviewed default")
	}
	// Zero is not "no floor". An accessor resolves it, so a caller cannot forget.
	if p.EffectiveMinFreeDisk() != contribution.MinFreeDiskBytesDefault {
		t.Fatalf("an unset reserve resolved to %d, not the default", p.EffectiveMinFreeDisk())
	}
	p, err = DecodeResourcePolicy([]byte(`{` + threeField + `,"minFreeDiskBytes":5368709120}`))
	if err != nil || p.MinFreeDiskBytes != 5<<30 || p.EffectiveMinFreeDisk() != 5<<30 {
		t.Fatalf("an owner-chosen reserve was not honoured: %v (%+v)", err, p)
	}
	for name, body := range map[string]string{
		"null reserve":        `{` + threeField + `,"minFreeDiskBytes":null}`,
		"duplicate reserve":   `{` + threeField + `,"minFreeDiskBytes":2147483648,"minFreeDiskBytes":2147483648}`,
		"noninteger reserve":  `{` + threeField + `,"minFreeDiskBytes":"10GB"}`,
		"fractional reserve":  `{` + threeField + `,"minFreeDiskBytes":1.5}`,
		"below floor":         `{` + threeField + `,"minFreeDiskBytes":1048576}`,
		"above ceiling":       `{` + threeField + `,"minFreeDiskBytes":2199023255552}`,
		"misspelled reserve":  `{` + threeField + `,"minFreeDisk":10737418240}`,
		"trailing after json": `{` + threeField + `,"minFreeDiskBytes":2147483648} {}`,
	} {
		if _, err := DecodeResourcePolicy([]byte(body)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestDiskReserveRoundTripsThroughConfig(t *testing.T) {
	c := Config{Version: 1, MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 1 << 30, IdleSeconds: 300}
	next := c.WithResourcePolicy(ResourcePolicy{MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 1 << 30, IdleSeconds: 300, MinFreeDiskBytes: 3 << 30})
	if next.MinFreeDiskBytes != 3<<30 {
		t.Fatal("the disk reserve did not reach the config")
	}
	if got := next.ResourcePolicy(); got.MinFreeDiskBytes != 3<<30 {
		t.Fatalf("the disk reserve did not come back out: %+v", got)
	}
	// Bounds are validated, so a broken client cannot write a configuration that
	// the next load would refuse.
	for _, bad := range []uint64{1, contribution.MinFreeDiskBytesFloor - 1, contribution.MinFreeDiskBytesCeiling + 1} {
		p := next.ResourcePolicy()
		p.MinFreeDiskBytes = bad
		if err := p.Validate(); err == nil {
			t.Errorf("out-of-range reserve %d accepted", bad)
		}
	}
}
