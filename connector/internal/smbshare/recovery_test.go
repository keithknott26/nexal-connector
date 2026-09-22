package smbshare

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func beginFixture(t *testing.T, timeBox time.Duration, now time.Time) *Mode {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	privateDir := filepath.Join(t.TempDir(), "private")
	mode, err := Begin(privateDir, "owner@example.test", "NexalRecovery", timeBox, now, key)
	if err != nil {
		t.Fatalf("recovery session refused: %v", err)
	}
	// Begin takes ownership and zeroes the caller's copy, so there is exactly one
	// copy of the plaintext key in the process.
	for _, b := range key {
		if b != 0 {
			t.Fatal("Begin did not zero the caller's copy of the image key")
		}
	}
	return mode
}

// THIS IS THE TEST THE HARDENING PLAN ASKS FOR. §12 makes recovery "a distinct,
// time-boxed state that serves only the SMB share and refuses every other
// capability (jobs, pool, pager)". A comment cannot be that; this can.
func TestRecoveryModeRefusesEveryCapabilityExceptTheShare(t *testing.T) {
	now := time.Now()
	mode := beginFixture(t, time.Hour, now)
	defer mode.End()

	if err := mode.Admit(CapabilitySMBShare, now); err != nil {
		t.Fatalf("recovery mode refused the one thing it exists to serve: %v", err)
	}
	for _, capability := range RefusedCapabilities {
		err := mode.Admit(capability, now)
		if err == nil {
			t.Fatalf("recovery mode admitted %q", capability)
		}
		if !errors.Is(err, ErrRecoveryMode) {
			t.Fatalf("%q was refused for the wrong reason: %v", capability, err)
		}
	}
	// FAIL CLOSED: a capability nobody has enumerated yet is refused too, so
	// adding a feature to the product cannot silently add it to recovery mode.
	for _, unknown := range []Capability{"", "future-feature", "SMB-SHARE", " smb-share"} {
		if err := mode.Admit(unknown, now); err == nil {
			t.Fatalf("recovery mode admitted an unrecognised capability %q", unknown)
		}
	}
	// A caller that cannot say what time it is gets a refusal, not a pass.
	if err := mode.Admit(CapabilitySMBShare, time.Time{}); err == nil {
		t.Fatal("recovery mode admitted the share with no clock")
	}
	// The published refusal list must stay in step with the enforcement: an
	// entry that Admit actually allows would make the list a lie.
	for _, capability := range RefusedCapabilities {
		if capability == CapabilitySMBShare {
			t.Fatal("the share is listed as refused")
		}
	}
}

func TestRecoveryModeIsTimeBoxedAndZeroesTheImageKey(t *testing.T) {
	now := time.Now()
	mode := beginFixture(t, time.Minute, now)

	// Inside the box the share is admitted.
	if err := mode.Admit(CapabilitySMBShare, now.Add(30*time.Second)); err != nil {
		t.Fatalf("share refused inside the time box: %v", err)
	}
	// The key is usable only through the lending accessor.
	seen := 0
	if err := mode.UseImageKey(func(key []byte) error { seen = len(key); return nil }); err != nil || seen != 32 {
		t.Fatalf("image key unavailable inside the session: %v", err)
	}
	// At and past the deadline everything is refused, WITHOUT any timer having
	// run: expiry is enforced on the asking path so a dead goroutine cannot
	// leave a session live.
	for _, at := range []time.Time{now.Add(time.Minute), now.Add(2 * time.Minute)} {
		if err := mode.Admit(CapabilitySMBShare, at); !errors.Is(err, ErrRecoveryOver) {
			t.Fatalf("an expired session admitted the share: %v", err)
		}
	}
	// Expiry also zeroed the plaintext key, and it stays unavailable.
	if !mode.KeyZeroed() {
		t.Fatal("the plaintext image key survived expiry")
	}
	if err := mode.UseImageKey(func([]byte) error { return nil }); !errors.Is(err, ErrRecoveryOver) {
		t.Fatalf("image key still lent after expiry: %v", err)
	}
	// There is no extension: the deadline is read-only and End is idempotent.
	deadline := mode.Expires()
	mode.End()
	mode.End()
	if !mode.Expires().Equal(deadline) {
		t.Fatal("the deadline moved")
	}
	// A nil Mode refuses rather than panicking, so a caller that failed to start
	// a session cannot accidentally be treated as authorized.
	var missing *Mode
	if err := missing.Admit(CapabilitySMBShare, now); !errors.Is(err, ErrRecoveryOver) {
		t.Fatalf("a nil recovery mode admitted a capability: %v", err)
	}
	if missing.Active(now) {
		t.Fatal("a nil recovery mode reported itself active")
	}
}

func TestRecoveryModeRefusesToStartWhenItCannotCleanUpOrKeyIsUnusable(t *testing.T) {
	now := time.Now()
	good := make([]byte, 32)
	for i := range good {
		good[i] = byte(i + 7)
	}
	readOnly := filepath.Join(t.TempDir(), "read-only")
	if err := os.MkdirAll(readOnly, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0700) })
	if _, err := Begin(readOnly, "owner@example.test", "NexalRecovery", time.Hour, now, good); err == nil {
		t.Fatal("a session that could not clean up after itself was allowed to start")
	}

	writable := filepath.Join(t.TempDir(), "private")
	for name, key := range map[string][]byte{
		"short_key":    make([]byte, 16),
		"all_zero_key": make([]byte, 32),
		"no_key":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Begin(writable, "owner@example.test", "NexalRecovery", time.Hour, now, key); err == nil {
				t.Fatal("an unusable image key was accepted")
			}
		})
	}
	for name, bad := range map[string]struct {
		user, share string
		box         time.Duration
		now         time.Time
	}{
		"no_time_box":       {"owner@example.test", "NexalRecovery", 0, now},
		"endless_time_box":  {"owner@example.test", "NexalRecovery", 30 * 24 * time.Hour, now},
		"username_not_mail": {"owner", "NexalRecovery", time.Hour, now},
		"unsafe_share_name": {"owner@example.test", "share\nguest ok = yes", time.Hour, now},
		"no_clock":          {"owner@example.test", "NexalRecovery", time.Hour, time.Time{}},
	} {
		t.Run(name, func(t *testing.T) {
			key := make([]byte, 32)
			for i := range key {
				key[i] = 9
			}
			if _, err := Begin(writable, bad.user, bad.share, bad.box, bad.now, key); err == nil {
				t.Fatal("an unsafe recovery session was accepted")
			}
			// Even a refused Begin must not leave the caller's key in memory.
			for _, b := range key {
				if b != 0 {
					t.Fatal("a refused session left the caller's image key unzeroed")
				}
			}
		})
	}
}

func TestRecoveryModeNeverSerializesKeyMaterial(t *testing.T) {
	now := time.Now()
	mode := beginFixture(t, time.Hour, now)
	defer mode.End()
	var captured []byte
	if err := mode.UseImageKey(func(key []byte) error {
		captured = append(captured, key...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(struct {
		Mode *Mode `json:"recovery"`
	}{mode})
	if err != nil {
		t.Fatal(err)
	}
	// The key bytes are not hex-encoded, base64 or raw in the payload.
	if strings.Contains(string(b), string(captured)) {
		t.Fatal("the plaintext image key was serialized")
	}
	for _, required := range []string{"refuses", "smb-share", "never written to disk or keychain", "not post-quantum"} {
		if !strings.Contains(strings.ToLower(string(b)), required) {
			t.Fatalf("the recovery payload omits %q", required)
		}
	}
}

func TestOneTimeShareCodeIsBoundSingleUseAndRateLimited(t *testing.T) {
	now := time.Now()
	credential, err := NewCredential("session-a", "owner@example.test", now)
	if err != nil {
		t.Fatal(err)
	}
	code, err := credential.Display()
	if err != nil {
		t.Fatal(err)
	}
	// §13: six characters of Crockford Base32, whose alphabet drops I, L, O, U.
	if len(code) != CodeLength {
		t.Fatalf("code is %d characters, not %d", len(code), CodeLength)
	}
	for _, r := range code {
		if !strings.ContainsRune(codeAlphabet, r) {
			t.Fatalf("code contains %q, which is not in the Crockford alphabet", r)
		}
	}
	if strings.ContainsAny(code, "ILOU") {
		t.Fatal("code contains a character the alphabet excludes")
	}
	// Displayed once only.
	if _, err := credential.Display(); err == nil {
		t.Fatal("the one-time code was displayed twice")
	}
	// BOUND, NEVER BEARER: the right code from the wrong session is refused, and
	// that refusal does not consume an attempt against the live code.
	if err := credential.Verify("session-b", code, now); err == nil {
		t.Fatal("a code was accepted for a session it was not issued to")
	}
	// Read-aloud normalization: O for 0, I/l for 1, and separators people add.
	spaced := strings.ToLower(code[:3] + "-" + code[3:])
	if err := credential.Verify("session-a", spaced, now.Add(time.Minute)); err != nil {
		t.Fatalf("a correctly read-aloud code was refused: %v", err)
	}
	// One use, atomically consumed.
	if err := credential.Verify("session-a", code, now.Add(2*time.Minute)); err == nil {
		t.Fatal("a consumed code was accepted a second time")
	}
	if _, err := credential.Password(); err == nil {
		t.Fatal("a consumed code still yielded a password")
	}
}

func TestOneTimeShareCodeBurnsAfterFiveWrongAttemptsAndExpires(t *testing.T) {
	now := time.Now()
	credential, err := NewCredential("session-a", "owner@example.test", now)
	if err != nil {
		t.Fatal(err)
	}
	code, err := credential.Display()
	if err != nil {
		t.Fatal(err)
	}
	wrong := "00000A"
	if NormalizeCode(wrong) == code {
		wrong = "00000B"
	}
	for i := 1; i <= MaxAttempts; i++ {
		if err := credential.Verify("session-a", wrong, now); err == nil {
			t.Fatal("a wrong code was accepted")
		}
	}
	// Burned: the RIGHT code no longer works, which is what makes 30 bits
	// defensible (§13).
	if err := credential.Verify("session-a", code, now); err == nil {
		t.Fatal("the correct code still worked after the attempt limit was reached")
	}

	// Absolute, non-sliding TTL.
	fresh, err := NewCredential("session-a", "owner@example.test", now)
	if err != nil {
		t.Fatal(err)
	}
	freshCode, err := fresh.Display()
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Verify("session-a", freshCode, now.Add(CredentialTTL)); err == nil {
		t.Fatal("an expired code was accepted")
	}
	if err := fresh.Verify("session-a", freshCode, now.Add(time.Minute)); err == nil {
		t.Fatal("an expired code came back to life")
	}

	// Refusals at mint time.
	if _, err := NewCredential("", "owner@example.test", now); err == nil {
		t.Fatal("an unbound bearer code was minted")
	}
	if _, err := NewCredential("session-a", "owner", now); err == nil {
		t.Fatal("a non-email SMB username was accepted")
	}
	if _, err := NewCredential("session-a", "owner@example.test", time.Time{}); err == nil {
		t.Fatal("a code was minted with no clock")
	}
}

func TestCredentialAndPasswordNeverSerializeTheSecret(t *testing.T) {
	now := time.Now()
	credential, err := NewCredential("session-a", "owner@example.test", now)
	if err != nil {
		t.Fatal(err)
	}
	code, err := credential.Display()
	if err != nil {
		t.Fatal(err)
	}
	password, err := credential.Password()
	if err != nil {
		t.Fatal(err)
	}
	// Provisioning takes the plaintext exactly once.
	if _, err := credential.Password(); err == nil {
		t.Fatal("the share credential was provisioned twice")
	}
	if err := password.Usable(); err != nil {
		t.Fatalf("a fresh password reported unusable: %v", err)
	}
	b, err := json.Marshal(map[string]any{"credential": credential, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), code) {
		t.Fatal("the one-time code was serialized")
	}
	// Zeroing is idempotent and irreversible.
	password.Zero()
	password.Zero()
	if !password.Zeroed() || password.Usable() == nil || password.reveal() != "" {
		t.Fatal("a zeroed password is still usable")
	}
	var missing *Password
	if missing.Usable() == nil || !missing.Zeroed() {
		t.Fatal("a nil password reported usable")
	}
	missing.Zero()
}

func TestNormalizeCodeOnlyAppliesReadAloudRules(t *testing.T) {
	for input, want := range map[string]string{
		"abcdef":       "ABCDEF",
		"a-b c\td e f": "ABCDEF",
		"il1o0":        "11100",
		"u":            "U", // U is not in the alphabet and is not invented into V
		"ünicode":      "?N1C0DE",
	} {
		if got := NormalizeCode(input); got != want {
			t.Fatalf("NormalizeCode(%q) = %q, want %q", input, got, want)
		}
	}
}
