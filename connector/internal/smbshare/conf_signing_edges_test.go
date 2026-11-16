package smbshare

import (
	"strings"
	"testing"
	"time"
)

func TestSafeConfValueRejectsStructuralAndControlCharacters(t *testing.T) {
	for _, ok := range []string{"a", "My Share", strings.Repeat("a", 1024)} {
		if !safeConfValue(ok) {
			t.Errorf("safeConfValue(%q) = false, want true", ok)
		}
	}
	bad := map[string]string{
		"empty":        "",
		"too long":     strings.Repeat("a", 1025),
		"newline":      "a\nb",
		"carriage":     "a\rb",
		"nul":          "a\x00b",
		"tab control":  "a\tb",
		"delete":       "a\x7fb",
		"continuation": `a\`,
		"semicolon":    "a;b",
		"hash":         "a#b",
		"open bracket": "[global",
		"close":        "a]",
		"equals":       "a=b",
	}
	for name, v := range bad {
		if safeConfValue(v) {
			t.Errorf("%s: safeConfValue(%q) = true, want false", name, v)
		}
	}
}

func TestSigningTextRejectsUnsafeFieldsAndMounts(t *testing.T) {
	now := time.Now().UTC()
	base, _, _ := authorizedFixture(t, now)
	if text, err := base.signingText(); err != nil || !strings.HasPrefix(text, "nexal-recovery-authorization-v1|grant-1|") {
		t.Fatalf("valid grant: text=%q err=%v", text, err)
	}
	mutations := map[string]func(*RecoveryAuthorization){
		"empty grant":      func(a *RecoveryAuthorization) { a.GrantID = "" },
		"pipe in helper":   func(a *RecoveryAuthorization) { a.HelperHostID = "a|b" },
		"newline in nonce": func(a *RecoveryAuthorization) { a.Nonce = "a\nb" },
		"nul in username":  func(a *RecoveryAuthorization) { a.Username = "a\x00b" },
		"oversized field":  func(a *RecoveryAuthorization) { a.NetworkID = strings.Repeat("n", 1025) },
		"relative mount":   func(a *RecoveryAuthorization) { a.MountPath = "Volumes/x" },
		"unclean mount":    func(a *RecoveryAuthorization) { a.MountPath = "/Volumes/x/../y" },
		"trailing slash":   func(a *RecoveryAuthorization) { a.MountPath = "/Volumes/x/" },
		"root mount":       func(a *RecoveryAuthorization) { a.MountPath = "/" },
	}
	for name, mutate := range mutations {
		a := base
		mutate(&a)
		if text, err := a.signingText(); err == nil {
			t.Errorf("%s: accepted, text=%q", name, text)
		}
	}
}
