package config

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"nexal/connector/internal/throttle"
)

const threeField = `"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824,"idleSeconds":300`

// The upload fields go through the same strict decoder as everything else:
// exactly-once keys, no nulls, no unknown fields, no trailing JSON. The optional
// fields are optional in presence only, never in rigour.
func TestUploadPolicyStrictDecoder(t *testing.T) {
	p, err := DecodeResourcePolicy([]byte(`{` + threeField + `,"uploadMode":"manual","uploadLimitBytesPerSecond":1048576}`))
	if err != nil || p.UploadMode != UploadModeManual || p.UploadLimitBytesPerSecond != 1<<20 {
		t.Fatalf("valid manual upload policy rejected: %v (%+v)", err, p)
	}
	if p, err := DecodeResourcePolicy([]byte(`{` + threeField + `,"uploadMode":"unlimited"}`)); err != nil ||
		p.UploadMode != UploadModeUnlimited {
		t.Fatalf("explicit unlimited mode rejected: %v", err)
	}
	// The measured rate is echoed back by GET, so a client that reads the policy
	// and writes it unchanged must be accepted — and must not be able to assert
	// a measurement. Accepted, then discarded.
	p, err = DecodeResourcePolicy([]byte(`{` + threeField + `,"measuredUploadBytesPerSecond":9999999}`))
	if err != nil {
		t.Fatalf("a round-tripped GET response was rejected: %v", err)
	}
	if p.MeasuredUploadBytesPerSecond != 0 {
		t.Fatal("a client was allowed to assert a measured upload rate")
	}
	for name, body := range map[string]string{
		"unknown mode":            `{` + threeField + `,"uploadMode":"fast"}`,
		"uppercase mode":          `{` + threeField + `,"uploadMode":"Auto"}`,
		"null mode":               `{` + threeField + `,"uploadMode":null}`,
		"empty mode":              `{` + threeField + `,"uploadMode":""}`,
		"numeric mode":            `{` + threeField + `,"uploadMode":3}`,
		"duplicate mode":          `{` + threeField + `,"uploadMode":"auto","uploadMode":"unlimited"}`,
		"duplicate limit":         `{` + threeField + `,"uploadMode":"manual","uploadLimitBytesPerSecond":1048576,"uploadLimitBytesPerSecond":2097152}`,
		"null limit":              `{` + threeField + `,"uploadMode":"manual","uploadLimitBytesPerSecond":null}`,
		"string limit":            `{` + threeField + `,"uploadMode":"manual","uploadLimitBytesPerSecond":"1048576"}`,
		"float limit":             `{` + threeField + `,"uploadMode":"manual","uploadLimitBytesPerSecond":1.5e6}`,
		"negative limit":          `{` + threeField + `,"uploadMode":"manual","uploadLimitBytesPerSecond":-1}`,
		"manual without a limit":  `{` + threeField + `,"uploadMode":"manual"}`,
		"limit below the floor":   `{` + threeField + `,"uploadMode":"manual","uploadLimitBytesPerSecond":1024}`,
		"limit above the ceiling": `{` + threeField + `,"uploadMode":"manual","uploadLimitBytesPerSecond":2147483648}`,
		"limit without manual":    `{` + threeField + `,"uploadMode":"auto","uploadLimitBytesPerSecond":1048576}`,
		"limit with unlimited":    `{` + threeField + `,"uploadMode":"unlimited","uploadLimitBytesPerSecond":1048576}`,
		"unknown upload field":    `{` + threeField + `,"uploadCeilingBytesPerSecond":1048576}`,
		"trailing json":           `{` + threeField + `,"uploadMode":"auto"}{}`,
		"upload fields only":      `{"uploadMode":"auto"}`,
	} {
		if _, err := DecodeResourcePolicy([]byte(body)); err == nil {
			t.Errorf("accepted invalid upload policy (%s): %s", name, body)
		}
	}
}

// EffectiveUploadLimit is the one place consent becomes a number, and zero — no
// throttle at all — must be reachable only by explicit owner choice.
func TestEffectiveUploadLimitNeverDegradesToUnlimited(t *testing.T) {
	base := ResourcePolicy{MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 1 << 30, IdleSeconds: 300}
	auto := base
	if limit, source := auto.EffectiveUploadLimit(); limit != throttle.ConservativeBytesPerSecond || source == "" {
		t.Fatalf("unmeasured auto mode gave %d (%s), want the conservative default", limit, source)
	}
	measured := base
	measured.MeasuredUploadBytesPerSecond = 3 << 20
	if limit, _ := measured.EffectiveUploadLimit(); limit != 3<<20 {
		t.Fatalf("measured auto mode gave %d, want the measured rate", limit)
	}
	manual := base
	manual.UploadMode, manual.UploadLimitBytesPerSecond = UploadModeManual, 1<<20
	manual.MeasuredUploadBytesPerSecond = 9 << 20
	if limit, _ := manual.EffectiveUploadLimit(); limit != 1<<20 {
		t.Fatalf("manual mode gave %d; a measurement overrode the owner", limit)
	}
	unlimited := base
	unlimited.UploadMode = UploadModeUnlimited
	if limit, source := unlimited.EffectiveUploadLimit(); limit != 0 || source == "" {
		t.Fatalf("explicit unlimited gave %d (%s), want 0 with a stated reason", limit, source)
	}
}

// A configuration file written before the upload dimension existed must load, and
// must load as auto. An upgrade that silently unshaped the uplink would be the
// worst possible reading of a missing field.
func TestOldConfigFileLoadsAsAutoAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.json")
	base := Config{Version: 1, Coordinator: "https://example.invalid", Name: "test",
		Listen: "127.0.0.1:8788", MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20,
		IdleSeconds: 300}
	if err := Save(path, base); err != nil {
		t.Fatal(err)
	}
	// Saved with no upload keys at all: omitempty keeps an untouched config from
	// gaining empty strings it never asked for.
	raw, err := ReadPrivate(path, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"uploadMode", "uploadLimitBytesPerSecond", "measuredUploadBytesPerSecond"} {
		if _, ok := fields[key]; ok {
			t.Errorf("an untouched config was rewritten with %s", key)
		}
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.ResourcePolicy(); got.UploadMode != UploadModeAuto ||
		got.UploadLimitBytesPerSecond != 0 || got.MeasuredUploadBytesPerSecond != 0 {
		t.Fatalf("an old config did not read as auto: %+v", got)
	}
	// And the full shape survives a round trip once it is set.
	next := loaded.WithResourcePolicy(ResourcePolicy{MemoryLimitBytes: 256 << 20,
		ReserveMemoryBytes: 128 << 20, IdleSeconds: 300, UploadMode: UploadModeManual,
		UploadLimitBytesPerSecond: 1 << 20, MeasuredUploadBytesPerSecond: 2 << 20})
	if err := Save(path, next); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.ResourcePolicy(); got.UploadMode != UploadModeManual ||
		got.UploadLimitBytesPerSecond != 1<<20 || got.MeasuredUploadBytesPerSecond != 2<<20 {
		t.Fatalf("upload policy did not round trip: %+v", got)
	}
}
