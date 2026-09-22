// Package smbshare supervises ONE operator-provisioned Samba process serving
// ONE Time Machine destination for ONE time-boxed recovery session.
//
// WHY THIS EXISTS: HARDENING-PLAN §8 and §12 decide that remote Time Machine
// backups ride SMB3 with Apple's extensions (vfs_fruit) because AFP is removed
// in macOS 27, and that the SMB server is SHIPPED AND SUPERVISED, never written
// here — "Ship Samba with vfs_fruit and supervise it the way cloudflared is
// already supervised — tunnel.go pins by SHA-256, allowlists and quarantines.
// Reuse that pattern exactly." This package is that reuse: the shape of Pin,
// Validate, BuildArgs, CleanEnv, Observe and Run deliberately mirrors
// internal/tunnel rather than inventing a second supervision model.
//
// WHAT THIS PACKAGE DOES NOT DO, ON PURPOSE:
//   - It does not implement SMB. No packet of SMB is parsed or produced here.
//   - It does not install, download or update Samba. A binary that is not
//     already present, executable and digest-matching is a refusal, never a
//     fetch.
//   - It does not claim post-quantum anything. SMB3 encryption is AES-GCM with a
//     CLASSICAL key agreement (§8, "Transport"). Over the cloudflared tunnel the
//     tunnel gets PQ key agreement; over raw LAN SMB it does not, and Status
//     says so in fixed words that no observation can upgrade.
//   - It does not touch the image encryption key. The share credential and the
//     APFS image key are separate secrets (§9); see recovery.go for the
//     ephemeral key handling.
package smbshare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/contribution"
)

// Pin is the provenance record for the shipped Samba build. Every field is
// required; a digest alone is not evidence of publisher identity, which is why
// the official artifact URL and the human verification note are recorded next to
// it exactly as tunnel.Tunnel does.
type Pin struct {
	// Binary is the absolute path to smbd. There is no PATH lookup: an
	// attacker-writable directory earlier on PATH must not be able to choose
	// which "smbd" this machine supervises.
	Binary string `json:"binary"`
	// SHA256 is the lowercase hex digest of that exact file, re-verified on
	// every Check AND again immediately before launch.
	SHA256 string `json:"sha256"`
	// PasswdBinary/PasswdSHA256 pin smbpasswd, which provisions the one-time
	// share credential into a private passdb. It is pinned separately rather than
	// derived from Binary's directory: "the sibling of the thing I trust" is not
	// a trust statement, and this is the process the plaintext credential is fed
	// to on stdin.
	PasswdBinary string `json:"passwdBinary"`
	PasswdSHA256 string `json:"passwdSha256"`
	// Version is the exact tested Samba release, e.g. 4.21.3.
	Version string `json:"version"`
	// Architecture must equal this host's GOARCH: a pin for another
	// architecture is a pin for a file this host is not running.
	Architecture string `json:"architecture"`
	// SourceURL must be an official samba.org release artifact naming Version.
	SourceURL string `json:"sourceUrl"`
	// VerificationMethod records how provenance was checked and what evidence
	// is MISSING. It is free text because the honest answer usually includes a
	// gap, and a boolean cannot carry a gap.
	VerificationMethod string `json:"verificationMethod"`
}

// Share is the single Time Machine destination. It carries no password: the
// one-time share credential lives in Credential (recovery.go) and reaches Samba
// only over a stdin pipe, never argv, never smb.conf, never Status.
type Share struct {
	// Name is the SMB share name recoveryOS will show.
	Name string `json:"name"`
	// Path is the absolute directory served. It is created by the caller, not
	// by this package: a supervisor that silently created the backing directory
	// could serve an empty share where the operator meant to serve a volume.
	Path string `json:"path"`
	// Username is the user's email address (§9: "the SMB username is the user's
	// email address"). recoveryOS has no NexAl software, so the only credential
	// it can carry is one a human types.
	Username string `json:"username"`
	// Port is the SMB port. 445 is the only port recoveryOS will try, so a
	// different port is allowed for tests and for an unprivileged local
	// rehearsal, never presented as a working recovery destination.
	Port uint16 `json:"port"`
	// MaxSizeBytes caps the sparsebundle Time Machine will grow, passed to
	// `fruit:time machine max size`. Required: an uncapped Time Machine
	// destination fills the host's volume, which on a friend's Mac is somebody
	// else's disk.
	MaxSizeBytes uint64 `json:"maxSizeBytes"`
	// TimeBox is the hard ceiling on how long the share serves. Run stops the
	// process when it elapses; there is no extension and no sliding renewal.
	TimeBox time.Duration `json:"timeBox"`
	// ScratchBytes is the streamed-restore cache requirement checked BEFORE
	// start (§9: "Scratch space is a hard prerequisite ... Check for it before
	// starting, not three hours in").
	ScratchBytes uint64 `json:"scratchBytes"`
}

const (
	// MinTimeBox/MaxTimeBox bound the time box. A restore of hundreds of GB can
	// legitimately run for hours, so the ceiling is generous; it is still a
	// ceiling, because "serves only until someone remembers to stop it" is not
	// a time-boxed state.
	//
	// The floor is one second rather than a plausible session length on purpose:
	// its job is to refuse zero and negative (which would mean "no box"), a
	// few-second box is a legitimate rehearsal, and a floor of minutes would make
	// the expiry path untestable without a fake clock inside context.WithTimeout.
	MinTimeBox = time.Second
	MaxTimeBox = 12 * time.Hour
	// DefaultTimeBox is what the CLI uses when the operator does not choose.
	DefaultTimeBox = 4 * time.Hour
	// MinScratchBytes is the floor for the streamed-restore cache. §9 says
	// "tens of GB"; 32 GiB is the smallest number that honestly satisfies that.
	MinScratchBytes = 32 << 30
	// maxBinaryBytes bounds hashing work on a path that turned out to be
	// something other than the Samba build.
	maxBinaryBytes = 512 << 20
	// TransportNote is the ONLY transport claim this package makes. It is a
	// constant so no code path can widen it into a post-quantum claim.
	TransportNote = "SMB3 encryption is AES-128/256-GCM with a CLASSICAL key agreement. Raw LAN SMB is NOT post-quantum; only a cloudflared-tunnelled path has post-quantum key agreement, and this share does not run over one."
	// PolicyNote and VerificationGap are the fixed honesty strings Status
	// carries, in the spirit of tunnel.Evidence.Policy/VerificationGap.
	PolicyNote      = "one supervised, SHA-256-pinned Samba process; SMB3 only, encryption and signing required, guest and SMB1 refused; one share, one user, one time box"
	VerificationGap = "No macOS Time Machine interoperability run, no Samba publisher signature verification, no live client-negotiated dialect or encryption attestation, and no R2/JuiceFS backing store. Health is a local reachability probe, not proof that Time Machine accepted this destination."
)

// Capability names appear in Status so an operator can see that the share is the
// only thing this mode serves. See recovery.go for the enforcement.
var (
	versionRE = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}\.[0-9]{1,2}$`)
	// Share names are deliberately narrower than Samba allows. The name is
	// interpolated into a generated smb.conf section header, so anything that
	// could close a section, start a directive or add a line is refused rather
	// than escaped — there is no escaping story in smb.conf worth trusting.
	shareNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)
	// Username is an email address, checked for shape and for smb.conf safety,
	// not for deliverability. A local part that could carry a comment character
	// or a newline is a config-injection vector, so the accepted set is tight.
	emailRE = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+/=?^_` + "`" + `{|}~.-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
)

// safeConfValue refuses any value that could escape its line in smb.conf. This
// is the whole injection defence: refuse, never escape.
func safeConfValue(s string) bool {
	if s == "" || len(s) > 1024 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if c < 0x20 || c == 0x7f || c == '\n' || c == '\r' {
			return false
		}
	}
	// A value containing these cannot be distinguished from structure by
	// Samba's parser (continuation, comment, new section, new assignment).
	return !strings.ContainsAny(s, "\\;#[]=")
}

// ValidateShare refuses a half-specified or unsafe share before anything is
// written or launched.
func ValidateShare(s Share) error {
	if !shareNameRE.MatchString(s.Name) {
		return errors.New("share name must be 1–32 characters of letters, digits, dot, dash or underscore")
	}
	if !filepath.IsAbs(s.Path) || s.Path != filepath.Clean(s.Path) || !safeConfValue(s.Path) {
		return errors.New("share path must be an absolute, clean, smb.conf-safe directory path")
	}
	st, err := os.Lstat(s.Path)
	if err != nil || !st.IsDir() {
		return errors.New("share path must already exist as a directory; this supervisor never creates the backing store")
	}
	// A world-writable backing directory on a friend's Mac means any local
	// account can alter the backup bytes. That is not a share, it is a mailbox.
	if st.Mode().Perm()&0022 != 0 {
		return errors.New("share path must not be group- or world-writable")
	}
	if !emailRE.MatchString(s.Username) || !safeConfValue(s.Username) || len(s.Username) > 254 {
		return errors.New("share username must be the owner's email address")
	}
	if s.Port == 0 {
		return errors.New("share port required; recoveryOS only reaches 445")
	}
	if s.MaxSizeBytes < 1<<30 || s.MaxSizeBytes > 64<<40 {
		return errors.New("Time Machine maximum size must be between 1 GiB and 64 TiB; an uncapped destination fills the host volume")
	}
	if s.TimeBox < MinTimeBox || s.TimeBox > MaxTimeBox {
		return fmt.Errorf("time box must be between %s and %s; recovery mode is a time-boxed state, not a service", MinTimeBox, MaxTimeBox)
	}
	if s.ScratchBytes < MinScratchBytes {
		return fmt.Errorf("streamed restore needs at least %d GiB of scratch space declared", MinScratchBytes>>30)
	}
	return nil
}

// validateBinary is the allowlist-and-pin check applied to every executable this
// package will run: absolute path, regular file, executable, not group/world
// writable, bounded, and hashing to the exact pinned digest.
func validateBinary(path, digest string) error {
	if !filepath.IsAbs(path) {
		return errors.New("samba binary paths must be absolute; no PATH lookup is permitted")
	}
	raw, err := hex.DecodeString(digest)
	if err != nil || len(raw) != 32 {
		return errors.New("pinned samba binary SHA256 required")
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 ||
		st.Mode().Perm()&0111 == 0 || st.Size() > maxBinaryBytes {
		return errors.New("pinned binary must be regular, executable, bounded and not group/world writable")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot read pinned binary")
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) || !actual.Mode().IsRegular() ||
		actual.Mode().Perm()&0022 != 0 || actual.Mode().Perm()&0111 == 0 || actual.Size() > maxBinaryBytes {
		return errors.New("pinned binary changed during open")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(f, maxBinaryBytes+1)); err != nil {
		return errors.New("cannot hash pinned binary")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), digest) {
		return errors.New("samba integrity check failed")
	}
	return nil
}

// Validate re-derives every provenance claim and re-hashes both binaries. It is
// called by Check AND again immediately before launch, because a file that
// hashed correctly a second ago is not the file that will be executed.
func Validate(p Pin) error {
	if !versionRE.MatchString(p.Version) {
		return errors.New("exact tested Samba release version required")
	}
	if p.Architecture != runtime.GOARCH {
		return errors.New("pinned samba architecture does not match this host")
	}
	u, err := url.Parse(p.SourceURL)
	if err != nil || u.Scheme != "https" || u.Host != "download.samba.org" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" ||
		!strings.HasPrefix(u.Path, "/pub/samba/") || strings.Contains(u.Path, "..") ||
		!strings.Contains(u.Path, "samba-"+p.Version) {
		return errors.New("official samba.org release artifact source URL naming the pinned version required")
	}
	if strings.TrimSpace(p.VerificationMethod) == "" || len(p.VerificationMethod) > 512 {
		return errors.New("record provenance verification method and any missing publisher evidence")
	}
	if p.Binary == p.PasswdBinary {
		return errors.New("smbd and smbpasswd must be distinct pinned binaries")
	}
	if err := validateBinary(p.Binary, p.SHA256); err != nil {
		return err
	}
	return validateBinary(p.PasswdBinary, p.PasswdSHA256)
}

// BuildArgs is the only launch policy. Samba's own configuration file is the
// generated one and nothing else: no ambient /etc/smb.conf, no operator-supplied
// options, no registry configuration, no daemon fork that would escape
// supervision.
func BuildArgs(confPath string) ([]string, error) {
	if !filepath.IsAbs(confPath) {
		return nil, errors.New("absolute private configuration path required")
	}
	return []string{
		"--configfile=" + confPath,
		// Foreground + no process group keeps smbd a supervised child that dies
		// with this process rather than a daemon that outlives the time box.
		"--foreground",
		"--no-process-group",
		// Diagnostics to stdout so the bounded line reader sees them; nothing is
		// written to a log file that could retain share diagnostics.
		"--debug-stdout",
	}, nil
}

// BuildPasswordArgs provisions the one-time share credential. `-s` makes
// smbpasswd read the password from stdin: the credential must never appear in
// argv, where any local process can read it from the process table.
func BuildPasswordArgs(confPath, username string) ([]string, error) {
	if !filepath.IsAbs(confPath) {
		return nil, errors.New("absolute private configuration path required")
	}
	if !emailRE.MatchString(username) {
		return nil, errors.New("share username must be the owner's email address")
	}
	return []string{"--configfile=" + confPath, "-a", "-s", username}, nil
}

// CleanEnv allows no SMB_CONF_PATH, LIBSMB_*, LD_*/DYLD_* or inherited HOME, so
// no ambient environment value can replace the generated configuration or
// preload code into the supervised process.
func CleanEnv() []string {
	return []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
}

// ConfPath and stateDir keep every generated artifact inside the connector's
// private 0700 directory, next to the configuration, so cleanup is one tree.
func ConfPath(privateDir string) string { return filepath.Join(privateDir, "smb.conf") }
func stateDir(privateDir string) string { return filepath.Join(privateDir, "smb-state") }

// RenderConf produces a minimal smb.conf. Minimal is a security property here:
// every directive present is one that either enables Time Machine or closes
// something, and there is no include, no registry backend and no printing stack.
//
// The Apple extensions are the point (§8: "macOS is picky about Time Machine
// destinations"): `vfs objects = catia fruit streams_xattr` in that order is
// what Samba's own documentation requires, and `fruit:time machine = yes` is
// what makes macOS offer the share as a Time Machine destination at all.
func RenderConf(s Share, privateDir string) (string, error) {
	if err := ValidateShare(s); err != nil {
		return "", err
	}
	if !filepath.IsAbs(privateDir) || !safeConfValue(privateDir) {
		return "", errors.New("absolute smb.conf-safe private directory required")
	}
	state := stateDir(privateDir)
	// Time Machine reads the size cap in Samba's own "<n>T"/"<n>G" form.
	size := strconv.FormatUint(s.MaxSizeBytes>>30, 10) + "G"
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	w("# Generated by the Nexal connector for ONE time-boxed recovery session.")
	w("# Do not edit: this file is rewritten on every start and removed on stop.")
	w("[global]")
	w("\tserver role = standalone server")
	w("\tsecurity = user")
	// SMB3 floor on both sides. SMB1 is not merely deprecated, it is the dialect
	// that makes guest access and unencrypted sessions reachable.
	w("\tserver min protocol = SMB3_11")
	w("\tserver max protocol = SMB3_11")
	w("\tclient min protocol = SMB3_11")
	// Encryption and signing are REQUIRED, not offered. A destination that can
	// be talked to in the clear is a destination whose bytes can be read off the
	// LAN even though the image itself is encrypted.
	w("\tserver smb encrypt = required")
	w("\tserver signing = mandatory")
	w("\tsmb ports = %d", s.Port)
	// No NetBIOS, no browser election, no WINS: recoveryOS finds the share over
	// mDNS/SMB directly, and NBT is an extra unauthenticated attack surface.
	w("\tdisable netbios = yes")
	w("\tsmb2 leases = yes")
	// Anonymous everything off. `map to guest = never` is what actually stops a
	// bad password from silently becoming a guest session.
	w("\tguest ok = no")
	w("\tmap to guest = never")
	w("\trestrict anonymous = 2")
	w("\tnull passwords = no")
	// Exactly one account may exist, in a private passdb inside the connector's
	// 0700 directory. The host's real accounts are not consulted.
	w("\tpassdb backend = tdbsam:%s", filepath.Join(state, "passdb.tdb"))
	w("\tprivate dir = %s", state)
	w("\tstate directory = %s", state)
	w("\tcache directory = %s", state)
	w("\tlock directory = %s", state)
	w("\tpid directory = %s", state)
	w("\tncalrpc dir = %s", filepath.Join(state, "ncalrpc"))
	// No printing stack, no spoolss, no printer enumeration.
	w("\tload printers = no")
	w("\tprintcap name = /dev/null")
	w("\tdisable spoolss = yes")
	w("\tshow add printer wizard = no")
	// Diagnostics stay on stdout for the bounded reader; nothing lands in a file.
	w("\tlog level = 1")
	w("\tmax log size = 0")
	w("\tlogging = stdout")
	// Apple extensions, global half.
	w("\tvfs objects = catia fruit streams_xattr")
	w("\tfruit:aapl = yes")
	w("\tfruit:model = MacSamba")
	w("\tfruit:metadata = stream")
	w("\tfruit:posix_rename = yes")
	w("\tfruit:veto_appledouble = no")
	w("\tfruit:wipe_intentionally_left_blank_rfork = yes")
	w("\tfruit:delete_empty_adfiles = yes")
	w("\tfruit:nfs_aces = no")
	w("\tfruit:zero_file_id = yes")
	// UNIX extensions off: macOS clients behave badly with them on a Time
	// Machine destination, and they widen what a client can ask the server for.
	w("\tunix extensions = no")
	w("")
	w("[%s]", s.Name)
	w("\tcomment = Nexal recovery destination (time-boxed)")
	w("\tpath = %s", s.Path)
	w("\tbrowseable = yes")
	w("\tread only = no")
	// ONE user, named explicitly. `valid users` is the enforcement that makes
	// this share the owner's and nobody else's, including the friend who owns
	// the hardware.
	w("\tvalid users = %s", s.Username)
	w("\tguest ok = no")
	w("\tvfs objects = catia fruit streams_xattr")
	w("\tfruit:time machine = yes")
	w("\tfruit:time machine max size = %s", size)
	w("\tea support = yes")
	w("\tdurable handles = yes")
	w("\tkernel oplocks = no")
	w("\tkernel share modes = no")
	w("\tposix locking = no")
	w("\tinherit permissions = yes")
	return b.String(), nil
}

// WriteConf writes the rendered configuration atomically at 0600 through the
// hardened helper (symlink-race safe, private directory enforced, fsynced).
// Nothing here writes a secret: the credential goes to smbpasswd over stdin, and
// this function refuses to write anything that looks like one.
func WriteConf(privateDir string, s Share, forbidden ...[]byte) (string, error) {
	text, err := RenderConf(s, privateDir)
	if err != nil {
		return "", err
	}
	// Belt and braces: if a future change ever threaded a secret into Share,
	// this is the check that stops it reaching disk.
	for _, secret := range forbidden {
		if len(secret) > 0 && strings.Contains(text, string(secret)) {
			return "", errors.New("refusing to write a generated configuration containing a credential")
		}
	}
	path := ConfPath(privateDir)
	if err := config.AtomicPrivate(path, []byte(text)); err != nil {
		return "", err
	}
	return path, nil
}

// CheckScratch enforces §9's hard prerequisite up front. An unmeasurable volume
// is a refusal, not an assumption: "unknown free space" on someone else's Mac is
// exactly the case where guessing costs three hours of restore.
func CheckScratch(path string, required uint64) (uint64, error) {
	return checkScratch(contribution.FreeDiskBytes, path, required)
}

// checkScratch takes the measurement function so a test can exercise the
// SATISFIED branch. A CI container commonly has a few GiB free, which is less
// than the §9 floor, so without this seam the only reachable outcome in tests
// would be the refusal — and the whole supervision path behind it would never
// run. The production caller always passes the real measurement.
func checkScratch(measure func(string) (uint64, error), path string, required uint64) (uint64, error) {
	free, err := measure(path)
	if err != nil {
		return 0, errors.New("cannot measure free space for the restore cache; refusing to start a restore that may run out of scratch space hours in")
	}
	if free < required {
		return free, fmt.Errorf("restore cache needs %d GiB free and this volume has %d GiB", required>>30, free>>30)
	}
	return free, nil
}

// Check validates the pin and confirms the running binary reports the pinned
// version. Like tunnel.Check it establishes nothing and serves nothing.
func Check(ctx context.Context, p Pin) (Status, error) {
	st := Status{Version: p.Version, Architecture: p.Architecture, SHA256: p.SHA256,
		SourceURL: p.SourceURL, VerificationMethod: p.VerificationMethod,
		Protocol: "SMB3.1.1", Transport: TransportNote, Policy: PolicyNote, VerificationGap: VerificationGap}
	if err := Validate(p); err != nil {
		return st, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.Binary, "--version")
	cmd.WaitDelay = time.Second
	cmd.Env = CleanEnv()
	out := &limitWriter{limit: 8192}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return st, errors.New("pinned samba version check failed")
	}
	// Samba prints "Version 4.21.3". Accept that exact shape only; a binary that
	// answers --version with anything else is not the pinned build's CLI.
	got := strings.TrimSpace(string(out.data))
	if got != "Version "+p.Version && !strings.HasPrefix(got, "Version "+p.Version+" ") &&
		!strings.HasPrefix(got, "Version "+p.Version+"-") {
		return st, errors.New("samba version differs from pin")
	}
	st.Configured = true
	return st, nil
}

type limitWriter struct {
	data  []byte
	limit int
}

func (w *limitWriter) Write(b []byte) (int, error) {
	if len(w.data)+len(b) > w.limit {
		return 0, errors.New("diagnostic limit exceeded")
	}
	w.data = append(w.data, b...)
	return len(b), nil
}
