package config

import (
	"fmt"
	"testing"
)

func TestResourcePolicyStrictDecoder(t *testing.T) {
	good := `{"memoryLimitBytes":268435456,"reserveMemoryBytes":1073741824,"idleSeconds":300}`
	p, err := DecodeResourcePolicy([]byte(good))
	if err != nil || p.MemoryLimitBytes != 256<<20 || p.ReserveMemoryBytes != 1<<30 || p.IdleSeconds != 300 {
		t.Fatalf("valid policy rejected: %v", err)
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
	for _, p := range []ResourcePolicy{
		{64 << 20, 128 << 20, 30}, {8 << 30, 1 << 40, 86400},
	} {
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for n, p := range []ResourcePolicy{
		{0, 128 << 20, 30}, {8<<30 + 1, 128 << 20, 30},
		{64 << 20, 128<<20 - 1, 30}, {64 << 20, 1<<40 + 1, 30},
		{64 << 20, 128 << 20, 29}, {64 << 20, 128 << 20, 86401},
	} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			if p.Validate() == nil {
				t.Fatal("invalid limit accepted")
			}
		})
	}
}
