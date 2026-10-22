package config

import (
	"strings"
	"testing"
)

func validBaseConfig() Config { return savedConfig() }

func TestConfigValidateRejectsUnsafeCombinations(t *testing.T) {
	if err := validBaseConfig().Validate(); err != nil {
		t.Fatalf("base config must be valid: %v", err)
	}
	fp := strings.Repeat("a", 64)
	cases := map[string]func(*Config){
		"wrong version":             func(c *Config) { c.Version = 2 },
		"file secrets in prod":      func(c *Config) { c.DevSecrets = true },
		"marketplace enabled":       func(c *Config) { c.MarketplaceEnabled = true },
		"blank name":                func(c *Config) { c.Name = "   " },
		"overlong name":             func(c *Config) { c.Name = strings.Repeat("n", 81) },
		"bad enrollment schema":     func(c *Config) { c.Enrollment = &EnrollmentState{SchemaVersion: 1, Status: "paired"} },
		"unknown enrollment status": func(c *Config) { c.Enrollment = &EnrollmentState{SchemaVersion: 2, Status: "weird"} },
		"paired without account": func(c *Config) {
			c.Enrollment = &EnrollmentState{SchemaVersion: 2, Status: "paired", NetworkID: "n", DeviceID: fp, PairedAt: "t", ManagementURL: "https://m.example"}
		},
		"paired with bad fingerprint": func(c *Config) {
			c.Enrollment = &EnrollmentState{SchemaVersion: 2, Status: "joining", AccountID: "a", NetworkID: "n", DeviceID: "XYZ", PairedAt: "t", ManagementURL: "https://m.example"}
		},
	}
	for name, mutate := range cases {
		c := validBaseConfig()
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s: Validate accepted it", name)
		}
	}

	c := validBaseConfig()
	c.Enrollment = &EnrollmentState{SchemaVersion: 2, Status: "revoked"}
	if err := c.Validate(); err != nil {
		t.Errorf("a revoked enrollment needs no pairing fields: %v", err)
	}
	c.Enrollment = &EnrollmentState{SchemaVersion: 2, Status: "paired", AccountID: "a", NetworkID: "n", DeviceID: fp, PairedAt: "t", ManagementURL: "https://m.example"}
	if err := c.Validate(); err != nil {
		t.Errorf("complete paired enrollment rejected: %v", err)
	}
}
