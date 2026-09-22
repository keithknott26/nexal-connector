package drive

import (
	"crypto/mlkem"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nexal/connector/internal/config"
)

// keyFileVersion is the stored format version. An unknown version is refused
// rather than guessed at: a drive key that decrypts nothing is recoverable (the
// objects are still there), but a key file silently misparsed into the wrong
// bytes would report every object as corrupt.
const keyFileVersion = 1

// maxKeyFileBytes bounds the read. A seed is 64 bytes and base64 of it is 88, so
// this is generous by three orders of magnitude and still refuses a file that
// was replaced with something enormous.
const maxKeyFileBytes = 8 << 10

type keyFile struct {
	Version int    `json:"version"`
	Seed    string `json:"seed"`
	Created string `json:"createdAt"`
}

// KeyPath is the drive key's location, beside the configuration rather than
// inside it.
//
// It is a separate file for the same reason peers.json is: the configuration is
// rewritten on every policy change, and a rewrite that loses this file loses
// every object in the drive permanently. A file that is only ever created once
// and then read cannot be lost to a racing policy write.
func KeyPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "drive-key.json")
}

// LoadOrCreateKey returns this Mac's drive keypair, creating it on first use.
//
// The seed is stored, not the expanded key, because mlkem's seed is the
// canonical 64-byte form and re-deriving from it is deterministic. Storing the
// expanded private key would be larger with no benefit.
//
// THE CONSEQUENCE, stated where it cannot be missed: this file is the only thing
// that can decrypt this Mac's drive objects. It is not escrowed to the
// coordinator, and the coordinator cannot recover it. Losing it loses the data.
// That is the deliberate trade for the coordinator being unable to read the
// data, and any future escrow has to be an explicit, opt-in feature rather than
// a silent default.
func LoadOrCreateKey(configPath string) (*mlkem.DecapsulationKey768, error) {
	path := KeyPath(configPath)
	data, err := config.ReadPrivate(path, maxKeyFileBytes)
	switch {
	case err == nil:
		return decodeKey(data, path)
	case errors.Is(err, os.ErrNotExist):
		return createKey(path)
	default:
		// Includes the permission and symlink refusals ReadPrivate makes. A drive
		// key we cannot read safely must not be quietly replaced with a new one:
		// that would orphan every existing object while appearing to succeed.
		return nil, fmt.Errorf("drive: cannot read the drive key: %w", err)
	}
}

func decodeKey(data []byte, path string) (*mlkem.DecapsulationKey768, error) {
	if err := config.CheckJSONObject(data); err != nil {
		return nil, fmt.Errorf("drive: %s is not a JSON object; refusing to overwrite it", path)
	}
	var f keyFile
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return nil, fmt.Errorf("drive: %s is not a recognisable drive key file; refusing to overwrite it", path)
	}
	if f.Version != keyFileVersion {
		return nil, fmt.Errorf("drive: %s has version %d, but this connector understands version %d; upgrade the connector rather than deleting the key",
			path, f.Version, keyFileVersion)
	}
	seed, err := base64.StdEncoding.Strict().DecodeString(f.Seed)
	if err != nil || len(seed) != mlkem.SeedSize {
		return nil, fmt.Errorf("drive: the seed in %s is not a %d-byte value; refusing to overwrite it", path, mlkem.SeedSize)
	}
	defer clear(seed)
	key, err := mlkem.NewDecapsulationKey768(seed)
	if err != nil {
		return nil, fmt.Errorf("drive: the seed in %s is not a valid ML-KEM-768 seed", path)
	}
	return key, nil
}

func createKey(path string) (*mlkem.DecapsulationKey768, error) {
	key, err := mlkem.GenerateKey768()
	if err != nil {
		return nil, errors.New("drive: cannot generate a drive key")
	}
	seed := key.Bytes()
	defer clear(seed)
	body, err := json.Marshal(keyFile{
		Version: keyFileVersion,
		Seed:    base64.StdEncoding.EncodeToString(seed),
		Created: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, errors.New("drive: cannot encode the drive key")
	}
	defer clear(body)
	// AtomicPrivate writes 0600 through a temporary file in the same directory and
	// requires that directory to be 0700, so the key is never briefly world
	// readable and is never written through a symlink.
	if err := config.AtomicPrivate(path, body); err != nil {
		return nil, fmt.Errorf("drive: cannot store the drive key: %w", err)
	}
	return key, nil
}

// PublicKey returns the encapsulation key to seal to. Taking it from the
// decapsulation key rather than storing it separately means the two can never
// disagree -- a stored public key that had drifted from the private one would
// produce objects this Mac could not open.
func PublicKey(private *mlkem.DecapsulationKey768) *mlkem.EncapsulationKey768 {
	return private.EncapsulationKey()
}
