package runtimebridge

import (
	"bytes"
	"encoding/json"
	"io"
)

// LoadOwnerPolicy reads an existing owner-controlled private local file. It
// neither computes trust pins nor treats current file hashes as an approval.
func LoadOwnerPolicy(path string) (OwnerPolicy, error) {
	var policy OwnerPolicy
	data, err := readLocalFile(path, 64*1024, true, false)
	if err != nil || strictObject(data, &policy, "installation", "entry_sha256",
		"python_sha256", "config_sha256", "runtime_files_sha256") != nil {
		return OwnerPolicy{}, ErrPolicy
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil ||
		strictObject(raw["installation"], &policy.Installation, "python", "entry", "config") != nil {
		return OwnerPolicy{}, ErrPolicy
	}
	// A normal json.Unmarshal(map) silently accepts duplicate hash keys.
	d := json.NewDecoder(bytes.NewReader(raw["runtime_files_sha256"]))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return OwnerPolicy{}, ErrPolicy
	}
	seen := map[string]bool{}
	for d.More() {
		t, err = d.Token()
		name, ok := t.(string)
		if err != nil || !ok || seen[name] {
			return OwnerPolicy{}, ErrPolicy
		}
		seen[name] = true
		var digest string
		if d.Decode(&digest) != nil || !validDigest(digest) {
			return OwnerPolicy{}, ErrPolicy
		}
	}
	if t, err = d.Token(); err != nil || t != json.Delim('}') {
		return OwnerPolicy{}, ErrPolicy
	}
	if _, err = d.Token(); err != io.EOF {
		return OwnerPolicy{}, ErrPolicy
	}
	return policy, nil
}
