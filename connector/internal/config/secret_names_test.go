package config

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretNameAllowlist(t *testing.T) {
	uuid := "123e4567-e89b-12d3-a456-426614174000"
	for name, want := range map[string]bool{
		"admin": true, "host": true, "peer-identity": true, "mesh-credential": true,
		"enrollment-session-" + uuid: true,
		"":                           false, "Admin": false, "../host": false, "host/": false,
		"enrollment-session-":                             false,
		"enrollment-session-short":                        false,
		"enrollment-session-" + uuid + "x":                false,
		"enrollment-session-" + uuid[:35] + "/":           false,
		"enrollment-session-" + uuid[:35] + "\n":          false,
		"enrollment-session-" + uuid[:35] + "\\":          false,
		"enrollment-session-" + uuid[:8] + " " + uuid[9:]: false,
	} {
		if got := secretName(name); got != want {
			t.Errorf("secretName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestValidSecretBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    string
		want bool
	}{
		{"too short", strings.Repeat("a", 15), false},
		{"minimum", strings.Repeat("a", 16), true},
		{"maximum", strings.Repeat("a", 4096), true},
		{"too long", strings.Repeat("a", 4097), false},
		{"space", strings.Repeat("a", 15) + " ", false},
		{"newline", strings.Repeat("a", 15) + "\n", false},
		{"DEL", strings.Repeat("a", 15) + "\x7f", false},
		{"non-ASCII", strings.Repeat("a", 15) + "é", false},
		{"tilde", strings.Repeat("a", 15) + "~", true},
	} {
		if got := validSecret(tc.s); got != tc.want {
			t.Errorf("%s: validSecret = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFileSecretsDeleteIsIdempotentAndScoped(t *testing.T) {
	ctx := context.Background()
	s := FileSecrets{Dir: filepath.Join(t.TempDir(), "secrets")}
	token, err := RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "admin", token); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "admin"); err != nil {
		t.Fatalf("delete existing: %v", err)
	}
	if err := s.Delete(ctx, "admin"); err != nil {
		t.Fatalf("delete missing should succeed: %v", err)
	}
	if _, err := s.Get(ctx, "admin"); err == nil {
		t.Fatal("deleted credential still readable")
	}
	if s.Delete(ctx, "../admin") == nil {
		t.Fatal("delete accepted a name outside the allowlist")
	}
	if _, err := s.Get(ctx, "unknown"); err == nil {
		t.Fatal("get accepted a name outside the allowlist")
	}
}
