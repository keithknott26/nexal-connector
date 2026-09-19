package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// ResourcePolicy intentionally excludes identity, credentials, transport and
// public execution switches. Updating resource consent cannot enable dispatch.
type ResourcePolicy struct {
	MemoryLimitBytes   uint64 `json:"memoryLimitBytes"`
	ReserveMemoryBytes uint64 `json:"reserveMemoryBytes"`
	IdleSeconds        uint64 `json:"idleSeconds"`
}

func (c Config) ResourcePolicy() ResourcePolicy {
	return ResourcePolicy{c.MemoryLimitBytes, c.ReserveMemoryBytes, c.IdleSeconds}
}

func (p ResourcePolicy) Validate() error {
	if p.MemoryLimitBytes < 64<<20 || p.MemoryLimitBytes > 8<<30 {
		return errors.New("approved memory limit must be 64 MiB–8 GiB")
	}
	if p.ReserveMemoryBytes < 128<<20 || p.ReserveMemoryBytes > 1<<40 {
		return errors.New("owner memory reserve must be 128 MiB–1 TiB")
	}
	if p.IdleSeconds < 30 || p.IdleSeconds > 86400 {
		return errors.New("idle threshold must be 30–86400 seconds")
	}
	return nil
}

func (c Config) WithResourcePolicy(p ResourcePolicy) Config {
	c.MemoryLimitBytes, c.ReserveMemoryBytes, c.IdleSeconds =
		p.MemoryLimitBytes, p.ReserveMemoryBytes, p.IdleSeconds
	return c
}

// DecodeResourcePolicy accepts exactly the three required integer fields once.
// Null, duplicate keys, unknown fields and trailing JSON are rejected.
func DecodeResourcePolicy(body []byte) (ResourcePolicy, error) {
	var p ResourcePolicy
	invalid := errors.New("expected exactly memoryLimitBytes, reserveMemoryBytes and idleSeconds")
	d := json.NewDecoder(bytes.NewReader(body))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return p, invalid
	}
	fields := map[string]*uint64{"memoryLimitBytes": &p.MemoryLimitBytes,
		"reserveMemoryBytes": &p.ReserveMemoryBytes, "idleSeconds": &p.IdleSeconds}
	seen := make(map[string]bool)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		dst := fields[key]
		if err != nil || !ok || dst == nil || seen[key] {
			return p, invalid
		}
		seen[key] = true
		var number *uint64
		if err := d.Decode(&number); err != nil || number == nil {
			return p, invalid
		}
		*dst = *number
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') || len(seen) != 3 {
		return p, invalid
	}
	if d.Decode(new(any)) != io.EOF {
		return p, invalid
	}
	return p, p.Validate()
}
