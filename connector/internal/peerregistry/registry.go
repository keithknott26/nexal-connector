// Package peerregistry persists the peer membership that pool.Registry holds in
// memory.
//
// pool.Registry was documented as an authority the "embedding agent must
// persist", and nothing did: NewRegistry had no caller outside tests anywhere in
// the connector. Because pool.NewRing refuses any member that is not enrolled in
// a registry, the collective could never involve a second machine -- the ring's
// reduce-scatter/all-gather math and its authenticated transport both worked and
// were tested, but two real Macs had no way to record that they knew each other.
// This package closes that gap and nothing more.
//
// It deliberately does NOT live in internal/config. Peer membership is written
// on a different schedule than the rest of the configuration -- enrolling a peer
// must not race a policy change, and a config rewrite must not be able to drop a
// revocation -- so it gets its own file with its own atomic write.
package peerregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/pool"
)

// maxRegistryBytes bounds a read. Membership is a handful of records for a home
// pool; anything approaching this is corruption or a hostile file, not a pool.
const maxRegistryBytes = 1 << 20

// fileVersion is the on-disk schema version. An unknown version is refused
// rather than guessed at: silently misreading membership would mean admitting or
// dropping a peer, and both are security outcomes.
const fileVersion = 1

type file struct {
	Version int           `json:"version"`
	Members []pool.Member `json:"members"`
}

// Path is the registry file beside the given configuration file. It is derived
// from the config path rather than from the home directory so that the
// --config flag, which the CLI already threads through every command, keeps
// development and production membership separate without a second flag.
func Path(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "peers.json")
}

// Load reads persisted membership and rebuilds the registry.
//
// A missing file is not an error: a host that has never enrolled a peer has an
// empty registry, which is the correct starting state. Every other failure is
// reported, because a registry that silently came back empty would re-admit a
// revoked machine.
func Load(path string, clock func() time.Time) (*pool.Registry, error) {
	raw, err := config.ReadPrivate(path, maxRegistryBytes)
	if errors.Is(err, os.ErrNotExist) {
		return pool.NewRegistry(clock), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read peer registry: %w", err)
	}
	var f file
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&f); err != nil {
		return nil, fmt.Errorf("peer registry is not valid: %w", err)
	}
	if f.Version != fileVersion {
		return nil, fmt.Errorf("peer registry version %d is not supported", f.Version)
	}
	// RestoreRegistry re-derives every DeviceID from its public key and refuses a
	// mismatch, so an edited file cannot bind a hostile key to a trusted peer's
	// fingerprint.
	return pool.RestoreRegistry(clock, f.Members)
}

// Save writes the registry's membership atomically with 0600 permissions.
//
// Revoked members are included, because Snapshot includes them: a revocation
// that vanished on restart would let a removed machine enroll again as if it
// were new.
func Save(path string, registry *pool.Registry) error {
	if registry == nil {
		return errors.New("peer registry is required")
	}
	data, err := json.MarshalIndent(file{Version: fileVersion, Members: registry.Snapshot()}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode peer registry: %w", err)
	}
	if err := config.AtomicPrivate(path, append(data, '\n')); err != nil {
		return fmt.Errorf("write peer registry: %w", err)
	}
	return nil
}
