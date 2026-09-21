package config

import (
	"fmt"
	"path/filepath"
	"testing"

	"nexal/connector/internal/throttle"
)

func TestResourcePolicyStrictDecoder(t *testing.T) {
	good := `{"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824,"idleSeconds":300}`
	p, err := DecodeResourcePolicy([]byte(good))
	if err != nil || p.MemoryLimitBytes != 256<<20 || p.ReserveMemoryBytes != 1<<30 || p.IdleSeconds != 300 {
		t.Fatalf("valid policy rejected: %v", err)
	}
	// BACKWARD COMPATIBILITY, asserted rather than assumed: a body written by a
	// client that predates the upload dimension still decodes, and inherits auto
	// — the shaped default, not unlimited.
	if p.UploadMode != UploadModeAuto || p.UploadLimitBytesPerSecond != 0 {
		t.Fatalf("a three-field policy did not default to auto: %+v", p)
	}
	for _, body := range []string{
		`null`, `[]`, `{}`, good + `{}`,
		`{"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824}`,
		`{"memoryLimitBytes":null,"reserveMemoryBytes":1073741824,"idleSeconds":300}`,
		`{"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824,"idleSeconds":300,"idleSeconds":600}`,
		`{"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824,"idleSeconds":300,"marketplaceEnabled":true}`,
		`{"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824,"idleSeconds":3e2}`,
		`{"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824,"idleSeconds":"300"}`,
		`{"memoryLimitBytes":-1,"reserveMemoryBytes":1073741824,"idleSeconds":300}`,
	} {
		if _, err := DecodeResourcePolicy([]byte(body)); err == nil {
			t.Errorf("accepted invalid policy: %s", body)
		}
	}
}

func TestResourcePolicyBounds(t *testing.T) {
	// Keyed literals from here on: the upload dimension added fields, and a
	// positional literal would silently reinterpret an existing test's numbers.
	for _, p := range []ResourcePolicy{
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30},
		{MemoryLimitBytes: 8 << 30, ReserveMemoryBytes: 1 << 40, IdleSeconds: 86400},
		// Upload mode absent is the old shape and must stay valid: it reads as auto.
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30, UploadMode: ""},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30,
			UploadMode: UploadModeManual, UploadLimitBytesPerSecond: throttle.MinBytesPerSecond},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30,
			UploadMode: UploadModeManual, UploadLimitBytesPerSecond: throttle.MaxBytesPerSecond},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30,
			UploadMode: UploadModeUnlimited},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30,
			MeasuredUploadBytesPerSecond: throttle.ConservativeBytesPerSecond},
	} {
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for n, p := range []ResourcePolicy{
		{MemoryLimitBytes: 0, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30},
		{MemoryLimitBytes: 8<<30 + 1, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128<<20 - 1, IdleSeconds: 30},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 1<<40 + 1, IdleSeconds: 30},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 29},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 86401},
		// An unknown mode is a configuration error, not something to fold into a
		// default: the owner asked for a behaviour this binary cannot honour.
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30, UploadMode: "Auto"},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30, UploadMode: "throttled"},
		// Manual mode with no usable limit, and out-of-range manual limits.
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30, UploadMode: UploadModeManual},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30,
			UploadMode: UploadModeManual, UploadLimitBytesPerSecond: throttle.MinBytesPerSecond - 1},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30,
			UploadMode: UploadModeManual, UploadLimitBytesPerSecond: throttle.MaxBytesPerSecond + 1},
		// A limit no mode consults is a trap: the owner believes it applies.
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30,
			UploadMode: UploadModeAuto, UploadLimitBytesPerSecond: 1 << 20},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30,
			UploadMode: UploadModeUnlimited, UploadLimitBytesPerSecond: 1 << 20},
		// A measured rate outside the bounds would fail to persist later.
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30,
			MeasuredUploadBytesPerSecond: throttle.MinBytesPerSecond - 1},
		{MemoryLimitBytes: 64 << 20, ReserveMemoryBytes: 128 << 20, IdleSeconds: 30,
			MeasuredUploadBytesPerSecond: throttle.MaxBytesPerSecond + 1},
	} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			if p.Validate() == nil {
				t.Fatal("invalid limit accepted")
			}
		})
	}
}

// Discovery is absent by default, and absent means off: peer discovery joins
// multicast groups and publishes this host's addresses, which is an owner
// decision rather than something a binary starts doing on upgrade.
func TestDiscoveryDefaultsClosed(t *testing.T) {
	var absent *Discovery
	if absent.Enabled() {
		t.Fatal("a config without a discovery block enabled discovery")
	}
	if (&Discovery{}).Enabled() {
		t.Fatal("the zero Discovery value is not the closed state")
	}
	if err := absent.Validate(); err != nil {
		t.Fatalf("an absent discovery block must validate: %v", err)
	}
}

// A gate opened without the identity or the port it needs is a configuration
// error, not a silent no-op: a host that means to advertise must say what.
func TestDiscoveryRefusesHalfConfiguredGate(t *testing.T) {
	good := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for name, d := range map[string]*Discovery{
		"wan without fingerprint": {WANRendezvous: true, PeerPort: 7443},
		"lan without fingerprint": {LANDiscovery: true, PeerPort: 7443},
		"wan without port":        {WANRendezvous: true, DeviceFingerprint: good},
		"uppercase fingerprint":   {WANRendezvous: true, PeerPort: 7443, DeviceFingerprint: "AB" + good[2:]},
		"short fingerprint":       {WANRendezvous: true, PeerPort: 7443, DeviceFingerprint: good[:63]},
		"non-hex fingerprint":     {WANRendezvous: true, PeerPort: 7443, DeviceFingerprint: "zz" + good[2:]},
	} {
		if err := d.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	full := &Discovery{LANDiscovery: true, WANRendezvous: true, DeviceFingerprint: good, PeerPort: 7443}
	if err := full.Validate(); err != nil {
		t.Fatalf("a fully configured discovery block was refused: %v", err)
	}
	if !full.Enabled() {
		t.Fatal("a configured discovery block is not enabled")
	}
}

// The block survives a save/load round trip, and a config file that does not
// mention discovery still loads with discovery off.
func TestDiscoveryRoundTripsThroughConfigFile(t *testing.T) {
	base := Config{Version: 1, Coordinator: "https://example.invalid", Name: "test",
		Listen: "127.0.0.1:8788", MemoryLimitBytes: 256 << 20, ReserveMemoryBytes: 128 << 20,
		IdleSeconds: 300}
	path := filepath.Join(t.TempDir(), "private", "config.json")
	if err := Save(path, base); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Discovery != nil {
		t.Fatalf("discovery = %+v, want absent", loaded.Discovery)
	}
	base.Discovery = &Discovery{WANRendezvous: true, PeerPort: 7443,
		DeviceFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if err := Save(path, base); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Discovery == nil || !loaded.Discovery.WANRendezvous ||
		loaded.Discovery.PeerPort != 7443 ||
		loaded.Discovery.DeviceFingerprint != base.Discovery.DeviceFingerprint {
		t.Fatalf("discovery did not round trip: %+v", loaded.Discovery)
	}
	if loaded.Discovery.LANDiscovery {
		t.Error("an unset gate came back open")
	}
}
