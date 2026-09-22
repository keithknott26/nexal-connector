package smbshare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/config"
)

// SAMBA IS NOT INSTALLED IN CI, AND THESE TESTS DO NOT NEED IT.
//
// Everything below executes inert /bin/sh fixtures named smbd and smbpasswd,
// exactly as internal/tunnel's tests execute an inert cloudflared fixture. What
// is under test is the supervision contract — pinning, allowlisting, generated
// configuration, quarantine, health, time box, cleanup — none of which depends on
// the real Samba. What CANNOT be tested this way is whether macOS Time Machine
// accepts the generated smb.conf; VerificationGap says so in the code, and the
// handoff notes say so in prose.
const (
	fixtureVersion = "4.21.3"
	fixtureSource  = "https://download.samba.org/pub/samba/stable/samba-4.21.3.tar.gz"
	fixtureMethod  = "TEST FIXTURE ONLY; no samba.org signature or publisher verification performed"
)

// writeExecutable writes an inert script and returns its digest.
func writeExecutable(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// smbdScript answers --version like Samba and otherwise emits the supplied
// diagnostic line and stays alive until killed.
func smbdScript(line string) string {
	return "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Version " + fixtureVersion + "'; exit 0; fi\n" +
		"printf '%s\\n' '" + line + "'\nexec /bin/sleep 120\n"
}

// passwdScript consumes stdin like `smbpasswd -a -s` and records what it was
// given, so a test can prove the credential arrived on stdin and not in argv.
func passwdScript(record string) string {
	return "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'Version " + fixtureVersion + "'; exit 0; fi\n" +
		"{ printf 'argv:%s\\n' \"$*\"; printf 'stdin:'; cat; } > '" + record + "'\nexit 0\n"
}

type fixtureSet struct {
	pin    Pin
	share  Share
	dir    string
	record string
}

func fixture(t *testing.T, line string) fixtureSet {
	t.Helper()
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	served := filepath.Join(dir, "served")
	for _, d := range []string{binDir, served} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	record := filepath.Join(dir, "smbpasswd-input")
	smbd := filepath.Join(binDir, "smbd")
	smbpasswd := filepath.Join(binDir, "smbpasswd")
	pin := Pin{
		Binary:       smbd,
		SHA256:       writeExecutable(t, smbd, smbdScript(line)),
		PasswdBinary: smbpasswd,
		PasswdSHA256: writeExecutable(t, smbpasswd, passwdScript(record)),
		Version:      fixtureVersion, Architecture: runtime.GOARCH,
		SourceURL: fixtureSource, VerificationMethod: fixtureMethod,
	}
	share := Share{Name: "NexalRecovery", Path: served, Username: "owner@example.test",
		Port: 14450, MaxSizeBytes: 64 << 30, TimeBox: 2 * time.Minute, ScratchBytes: MinScratchBytes}
	return fixtureSet{pin: pin, share: share, dir: dir, record: record}
}

func TestPinnedBinariesAndOfficialProvenanceRequired(t *testing.T) {
	f := fixture(t, "waiting for connections")
	if err := Validate(f.pin); err != nil {
		t.Fatalf("valid pin rejected: %v", err)
	}
	st, err := Check(context.Background(), f.pin)
	if err != nil || !st.Configured || st.Verified || st.Attestation || st.Serving {
		t.Fatalf("%+v %v", st, err)
	}

	for name, mutate := range map[string]func(p *Pin){
		"wrong_smbd_digest":   func(p *Pin) { p.SHA256 = strings.Repeat("0", 64) },
		"wrong_passwd_digest": func(p *Pin) { p.PasswdSHA256 = strings.Repeat("0", 64) },
		"path_lookup":         func(p *Pin) { p.Binary = "smbd" },
		"passwd_path_lookup":  func(p *Pin) { p.PasswdBinary = "smbpasswd" },
		"same_binary_twice":   func(p *Pin) { p.PasswdBinary = p.Binary; p.PasswdSHA256 = p.SHA256 },
		"unofficial_source":   func(p *Pin) { p.SourceURL = "https://evil.test/samba-4.21.3.tar.gz" },
		"lookalike_host": func(p *Pin) {
			p.SourceURL = "https://download.samba.org.attacker.invalid/pub/samba/samba-4.21.3.tar.gz"
		},
		"userinfo_trick": func(p *Pin) {
			p.SourceURL = "https://download.samba.org@attacker.invalid/pub/samba/samba-4.21.3.tar.gz"
		},
		"plaintext_source":      func(p *Pin) { p.SourceURL = "http://download.samba.org/pub/samba/stable/samba-4.21.3.tar.gz" },
		"traversal_source":      func(p *Pin) { p.SourceURL = "https://download.samba.org/pub/samba/../samba-4.21.3.tar.gz" },
		"version_not_in_source": func(p *Pin) { p.SourceURL = "https://download.samba.org/pub/samba/stable/samba-4.99.9.tar.gz" },
		"query_redirect":        func(p *Pin) { p.SourceURL = fixtureSource + "?redirect=attacker" },
		"unpinned_version":      func(p *Pin) { p.Version = "latest" },
		"foreign_architecture":  func(p *Pin) { p.Architecture = "s390x" },
		"no_provenance_note":    func(p *Pin) { p.VerificationMethod = "  " },
	} {
		t.Run(name, func(t *testing.T) {
			pin := fixture(t, "waiting for connections").pin
			mutate(&pin)
			if err := Validate(pin); err == nil {
				t.Fatal("unsafe or ambiguous pin accepted")
			}
		})
	}

	t.Run("world_writable_binary", func(t *testing.T) {
		pin := fixture(t, "waiting for connections").pin
		if err := os.Chmod(pin.Binary, 0777); err != nil {
			t.Fatal(err)
		}
		if Validate(pin) == nil {
			t.Fatal("world-writable binary accepted")
		}
	})
	t.Run("version_differs_from_pin", func(t *testing.T) {
		pin := fixture(t, "waiting for connections").pin
		body := "#!/bin/sh\necho 'Version 4.19.0'\nexit 0\n"
		pin.SHA256 = writeExecutable(t, pin.Binary, body)
		if _, err := Check(context.Background(), pin); err == nil {
			t.Fatal("running version that differs from the pin accepted")
		}
	})
}

func TestShareValidationRefusesUnsafeAndHalfSpecifiedShares(t *testing.T) {
	base := fixture(t, "waiting for connections").share
	if err := ValidateShare(base); err != nil {
		t.Fatalf("valid share rejected: %v", err)
	}
	for name, mutate := range map[string]func(s *Share){
		"relative_path":     func(s *Share) { s.Path = "served" },
		"missing_path":      func(s *Share) { s.Path = "/nonexistent-nexal-share-path" },
		"username_not_mail": func(s *Share) { s.Username = "owner" },
		"no_port":           func(s *Share) { s.Port = 0 },
		"uncapped_size":     func(s *Share) { s.MaxSizeBytes = 0 },
		"no_time_box":       func(s *Share) { s.TimeBox = 0 },
		"forever_time_box":  func(s *Share) { s.TimeBox = 30 * 24 * time.Hour },
		"no_scratch":        func(s *Share) { s.ScratchBytes = 1 << 30 },
		"empty_name":        func(s *Share) { s.Name = "" },
		// smb.conf injection: each of these would add a directive or close the
		// section if the value were interpolated rather than refused.
		"name_newline":     func(s *Share) { s.Name = "share\n\tguest ok = yes" },
		"name_bracket":     func(s *Share) { s.Name = "share]\n[global" },
		"username_newline": func(s *Share) { s.Username = "owner@example.test\n\tguest ok = yes" },
		"username_comment": func(s *Share) { s.Username = "owner;guest@example.test" },
	} {
		t.Run(name, func(t *testing.T) {
			share := fixture(t, "waiting for connections").share
			mutate(&share)
			if err := ValidateShare(share); err == nil {
				t.Fatal("unsafe share accepted")
			}
			// The same refusal must hold at the rendering boundary, so no future
			// caller can reach RenderConf without ValidateShare.
			if _, err := RenderConf(share, t.TempDir()); err == nil {
				t.Fatal("unsafe share rendered into smb.conf")
			}
		})
	}
	t.Run("world_writable_backing_directory", func(t *testing.T) {
		share := fixture(t, "waiting for connections").share
		if err := os.Chmod(share.Path, 0777); err != nil {
			t.Fatal(err)
		}
		if ValidateShare(share) == nil {
			t.Fatal("world-writable backing directory accepted")
		}
	})
}

func TestGeneratedConfIsTimeMachineCapableAndFailsClosed(t *testing.T) {
	f := fixture(t, "waiting for connections")
	privateDir := filepath.Join(t.TempDir(), "private")
	path, err := WriteConf(privateDir, f.share)
	if err != nil {
		t.Fatal(err)
	}
	// Written through the hardened helper: 0600, regular, inside a 0700 dir.
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		t.Fatalf("generated smb.conf is not a private regular 0600 file: %v", err)
	}
	b, err := config.ReadPrivate(path, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	conf := string(b)

	// THE APPLE EXTENSIONS ARE THE POINT: without vfs_fruit and `fruit:time
	// machine = yes` macOS does not offer the share as a Time Machine
	// destination at all, which is the whole reason this package exists.
	for _, required := range []string{
		"vfs objects = catia fruit streams_xattr",
		"fruit:time machine = yes",
		"fruit:time machine max size = 64G",
		"fruit:aapl = yes",
		"fruit:metadata = stream",
		"server min protocol = SMB3_11",
		"client min protocol = SMB3_11",
		"server smb encrypt = required",
		"server signing = mandatory",
		"map to guest = never",
		"guest ok = no",
		"valid users = owner@example.test",
		"[NexalRecovery]",
	} {
		if !strings.Contains(conf, required) {
			t.Fatalf("generated smb.conf is missing %q", required)
		}
	}
	// And must NOT contain any of the things that would weaken it.
	for _, forbidden := range []string{
		"NT1", "SMB1", "smb encrypt = off", "guest ok = yes", "map to guest = bad user",
		"include =", "security = share", "null passwords = yes",
	} {
		if strings.Contains(conf, forbidden) {
			t.Fatalf("generated smb.conf contains %q", forbidden)
		}
	}
	// A credential must never be renderable into the file, even if a future
	// change threaded one into Share.
	if _, err := WriteConf(privateDir, f.share, []byte("owner@example.test")); err == nil {
		t.Fatal("WriteConf wrote a configuration containing a forbidden value")
	}
	if _, err := RenderConf(f.share, "relative-private-dir"); err == nil {
		t.Fatal("relative private directory accepted")
	}
}

func TestLaunchPolicyKeepsSecretsOutOfArgvAndEnvironment(t *testing.T) {
	args, err := BuildArgs("/private/smb.conf")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--configfile=/private/smb.conf", "--foreground", "--no-process-group", "--debug-stdout"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("%v", args)
	}
	if _, err := BuildArgs("relative"); err == nil {
		t.Fatal("relative configuration path accepted")
	}
	passwdArgs, err := BuildPasswordArgs("/private/smb.conf", "owner@example.test")
	if err != nil {
		t.Fatal(err)
	}
	// `-s` is what makes smbpasswd read stdin. Its absence would mean a prompt,
	// and any credential-bearing argument would mean the process table.
	if !reflect.DeepEqual(passwdArgs, []string{"--configfile=/private/smb.conf", "-a", "-s", "owner@example.test"}) {
		t.Fatalf("%v", passwdArgs)
	}
	if _, err := BuildPasswordArgs("/private/smb.conf", "not-an-email"); err == nil {
		t.Fatal("non-email share username accepted")
	}
	// No ambient override of the generated configuration or of the loader.
	t.Setenv("SMB_CONF_PATH", "/tmp/attacker-smb.conf")
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/tmp/evil.dylib")
	t.Setenv("HOME", "/tmp/attacker-home")
	for _, env := range CleanEnv() {
		if strings.Contains(env, "SMB") || strings.Contains(env, "LD_") ||
			strings.Contains(env, "DYLD") || strings.HasPrefix(env, "HOME=") {
			t.Fatalf("ambient override leaked into the supervised environment: %q", env)
		}
	}
}

func TestScratchPrerequisiteIsCheckedUpFront(t *testing.T) {
	dir := t.TempDir()
	// A requirement no volume can satisfy must be refused before anything runs.
	if _, err := CheckScratch(dir, 1<<62); err == nil {
		t.Fatal("impossible scratch requirement accepted")
	}
	// An unmeasurable path is a refusal, not an assumption.
	if _, err := CheckScratch(filepath.Join(dir, "no-such-path"), 1<<20); err == nil {
		t.Fatal("unmeasurable volume accepted")
	}
	if _, err := CheckScratch(dir, 1); err != nil {
		t.Fatalf("a one-byte requirement should be satisfiable: %v", err)
	}
}

func TestNoPostQuantumClaimForRawLANSMB(t *testing.T) {
	f := fixture(t, "waiting for connections")
	st, err := Check(context.Background(), f.pin)
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(st.Transport + " " + st.Policy)
	if !strings.Contains(low, "not post-quantum") {
		t.Fatal("status does not state that raw LAN SMB is not post-quantum")
	}
	for _, claim := range []string{"ml-kem", "kyber", "x25519mlkem768", "post-quantum key agreement is"} {
		if strings.Contains(strings.ToLower(st.Transport), claim) && !strings.Contains(low, "not post-quantum") {
			t.Fatalf("status claims post-quantum protection for raw LAN SMB: %q", claim)
		}
	}
	// No observation, however encouraging, can promote the status.
	st = Observe(st, []byte("SMB3 session encrypted with AES-256-GCM, post-quantum"), time.Now())
	if st.Verified || st.Attestation || st.Transport != TransportNote {
		t.Fatalf("a diagnostic changed the transport claim: %+v", st)
	}
}
