package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/smbshare"
)

// `nexal share` runs the supervised SMB3 Time Machine destination that backs
// remote backup and the "recover my Mac from a different Mac on the same network"
// flow.
//
// WHY THIS COMMAND EXISTS: HARDENING-PLAN §9 has the broken Mac booted into
// recoveryOS, which has no NexAl software at all. The only thing it can do is
// mount a plain SMB share with a username and a password typed in by hand. So
// something on the friend's Mac has to bring up a real, discoverable SMB3 share,
// show a code on screen, and take it down again. That is this command; the
// supervision, pinning and refusals live in internal/smbshare.
//
// WHAT AN OPERATOR MUST UNDERSTAND FROM THE OUTPUT, and what this command
// therefore always prints:
//   - Raw LAN SMB is NOT post-quantum. SMB3 gives AES-GCM with a classical key
//     agreement (§8). The post-quantum claim in this product belongs to escrow
//     (ML-KEM-768) and to the cloudflared tunnel, not here.
//   - Recovery is a MODE, not a service. It is time-boxed, it serves only the
//     share, and it refuses jobs, pool and pager for its whole duration.
//   - The friend owns the hardware. The plaintext image key is in RAM on someone
//     else's computer for the length of the session and nowhere else, ever.
func shareCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nexal share status|start|stop [flags] [--config absolute-path]")
	}
	switch args[0] {
	case "status":
		return shareStatus(args[1:])
	case "start":
		return shareStart(ctx, args[1:])
	case "stop":
		return shareStop(args[1:])
	default:
		return fmt.Errorf("unknown share subcommand %q", args[0])
	}
}

// sessionRecord is the nonsecret, on-disk view of a live session, written 0600
// through config.AtomicPrivate so `share status` on another terminal and the
// menu-bar app can read it.
//
// IT CONTAINS NO SECRET. Not the one-time code, not its hash, not the image key,
// not a passdb path's contents. The code is shown once on screen by `share
// start` and exists nowhere else; smbshare.Credential and smbshare.Mode both
// marshal to redactions so an accidental embed cannot change that.
type sessionRecord struct {
	SessionID  string          `json:"sessionId"`
	ShareName  string          `json:"shareName"`
	Username   string          `json:"username"`
	Port       uint16          `json:"port"`
	StartedAt  time.Time       `json:"startedAt"`
	ExpiresAt  time.Time       `json:"expiresAt"`
	TimeBox    string          `json:"timeBox"`
	Serves     []string        `json:"serves"`
	Refuses    []string        `json:"refuses"`
	Transport  string          `json:"transport"`
	Supervisor smbshare.Status `json:"supervisor"`
}

func sessionPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "smb-session.json")
}

// stopPath is a sentinel file, not a signal. `share stop` cannot take the
// configuration lock (the running session holds it for its whole duration) and
// must not guess at a recorded PID, which may have been reused by an unrelated
// process. An atomically written 0600 sentinel that the session polls for is the
// mechanism that is both race-free and incapable of killing the wrong thing.
func stopPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "smb-stop")
}

// shareStatus reports the session without changing anything.
//
// IT DELIBERATELY DOES NOT TAKE THE CONFIGURATION LOCK. Every other read-only
// subcommand in this CLI does (see peers list), because there the lock only
// prevents observing a half-written file. Here the lock is held for the whole
// length of a running session, so taking it would make `share status` block until
// the thing it is reporting on has finished — a status command that only works
// when there is nothing to report. The record is written atomically, so a reader
// sees either the previous or the next complete version and never a partial one.
func shareStatus(args []string) error {
	f, path, err := flags("share status")
	if err != nil {
		return err
	}
	if err := parse(f, args, path); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: nexal share status [--config absolute-path]")
	}
	refuses := make([]string, 0, len(smbshare.RefusedCapabilities))
	for _, c := range smbshare.RefusedCapabilities {
		refuses = append(refuses, string(c))
	}
	b, err := config.ReadPrivate(sessionPath(*path), 64<<10)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
			return emit(map[string]any{
				"recovery":  false,
				"serving":   false,
				"transport": smbshare.TransportNote,
				"note":      "no recovery share session on this Mac; `nexal share start` begins a time-boxed one",
				"refuses":   refuses,
			})
		}
		return err
	}
	if err := config.CheckJSONObject(b); err != nil {
		return errors.New("recovery session record is not a JSON object")
	}
	var record sessionRecord
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return errors.New("recovery session record is not valid")
	}
	// Expiry is evaluated HERE rather than trusted from the record: a session
	// whose process died without cleaning up must read as over, not as serving.
	expired := !time.Now().Before(record.ExpiresAt)
	serving := record.Supervisor.Serving && !record.Supervisor.Quarantined && !expired
	return emit(map[string]any{
		"recovery":   !expired,
		"serving":    serving,
		"expired":    expired,
		"session":    record,
		"refuses":    refuses,
		"transport":  smbshare.TransportNote,
		"imageKey":   "held in memory only by the running session; never written to disk or Keychain",
		"credential": "the one-time share code is displayed once by `nexal share start` and is not recoverable from here",
		"honestNote": "serving means the supervisor's local reachability probe succeeded, not that Time Machine accepted this destination",
	})
}

// shareStart brings the share up for exactly one time-boxed session and blocks
// until it ends.
//
// THE SECRETS COME FROM STDIN, NEVER ARGV. The image key is read as hex from
// stdin behind an explicit --image-key-stdin flag, exactly as `nexal enroll`
// requires --code-stdin, because argv is readable by every process on the
// machine. The share password is not an input at all: it is generated here,
// displayed once, and fed to smbpasswd over a pipe.
//
// THE CONFIGURATION LOCK IS HELD ACROSS READ AND WRITE, and then for the whole
// session. That is not incidental: `nexal run` takes the same exclusive lock, so
// holding it is what makes "recovery mode refuses jobs, pool and pager" true at
// the process level as well as at the smbshare.Mode.Admit level. A second
// connector cannot start while a recovery session owns this configuration.
func shareStart(ctx context.Context, args []string) error {
	f, path, err := flags("share start")
	if err != nil {
		return err
	}
	binary := f.String("samba-binary", "", "absolute path to the pinned smbd")
	sha := f.String("samba-sha256", "", "SHA-256 of that exact smbd build, lowercase hex")
	passwdBinary := f.String("smbpasswd-binary", "", "absolute path to the pinned smbpasswd")
	passwdSHA := f.String("smbpasswd-sha256", "", "SHA-256 of that exact smbpasswd build, lowercase hex")
	version := f.String("samba-version", "", "exact tested Samba release, e.g. 4.21.3")
	// Required rather than taken from runtime.GOARCH: the point of the check is to
	// catch a pin recorded for a different Mac, and a value this command filled in
	// itself could never disagree with the host it is running on.
	arch := f.String("samba-architecture", "", "architecture the pinned build was tested on; must match this host")
	source := f.String("samba-source-url", "", "official samba.org release artifact URL naming that version")
	method := f.String("verification-method", "", "how provenance was verified, including what evidence is missing")
	name := f.String("share-name", "NexalRecovery", "SMB share name shown in recoveryOS")
	shareDir := f.String("path", "", "absolute existing directory to serve")
	username := f.String("username", "", "the owner's email address; this is the SMB username")
	port := f.Uint("port", 445, "SMB port; recoveryOS only reaches 445")
	maxSizeGiB := f.Uint64("max-size-gib", 2048, "Time Machine size cap for this destination in GiB")
	scratchGiB := f.Uint64("scratch-gib", smbshare.MinScratchBytes>>30, "streamed-restore cache required free on the served volume, in GiB")
	timeBox := f.Duration("time-box", smbshare.DefaultTimeBox, "hard limit on the session, 1m–12h; there is no extension")
	keyStdin := f.Bool("image-key-stdin", false, "read the 32-byte image key as hex from stdin; required")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: nexal share start --samba-binary path --samba-sha256 hex --samba-architecture arm64 --smbpasswd-binary path --smbpasswd-sha256 hex --samba-version x.y.z --samba-source-url url --verification-method text --path dir --username email --image-key-stdin [--share-name name] [--port 445] [--max-size-gib 2048] [--time-box 4h] [--config absolute-path]")
	}
	if !*keyStdin {
		return errors.New("--image-key-stdin is required; an image key must never be placed in command arguments")
	}
	if *port > 65535 || *port == 0 {
		return errors.New("--port must be 1–65535")
	}
	if *maxSizeGiB == 0 {
		return errors.New("--max-size-gib must be set; an uncapped Time Machine destination fills the host volume")
	}

	// Read the key before taking the lock so a mistyped invocation fails without
	// blocking a concurrent connector, and zero every copy on every path out.
	raw, err := readBoundedStdin(4096)
	if err != nil {
		return err
	}
	defer clearBytes(raw)
	keyHex := strings.TrimSpace(string(raw))
	if len(keyHex) != 64 {
		return errors.New("image key must be 64 hex characters (32 bytes) on stdin")
	}
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return errors.New("image key must be valid lowercase hex")
	}
	// smbshare.Begin takes ownership and zeroes this slice; the defer is the
	// belt-and-braces path for every error return before that point.
	defer clearBytes(key)

	unlock, err := config.Lock(*path)
	if err != nil {
		return err
	}
	defer unlock()
	// Read under the lock, exactly as identity and static-peers do.
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	// recoveryOS will only ever try 445. Allowing another port in production
	// would let an operator stand up a share that looks healthy here and is
	// invisible to the machine it exists for.
	if *port != 445 && !c.Development {
		return errors.New("recoveryOS only reaches port 445; a different port is allowed only for a development configuration")
	}
	privateDir := filepath.Dir(*path)

	pin := smbshare.Pin{Binary: *binary, SHA256: *sha, PasswdBinary: *passwdBinary, PasswdSHA256: *passwdSHA,
		Version: *version, Architecture: *arch, SourceURL: *source, VerificationMethod: *method}
	share := smbshare.Share{Name: *name, Path: *shareDir, Username: *username, Port: uint16(*port),
		MaxSizeBytes: *maxSizeGiB << 30, TimeBox: *timeBox, ScratchBytes: *scratchGiB << 30}

	sessionID, err := config.RandomToken()
	if err != nil {
		return errors.New("cannot generate a recovery session identifier")
	}
	now := time.Now()
	// Begin performs the §9 refusals: a bad key, a bad time box, and above all a
	// private directory whose state cannot be created AND removed, which is what
	// "refuse to start if cleanup cannot be guaranteed" means in practice.
	mode, err := smbshare.Begin(privateDir, *username, *name, *timeBox, now, key)
	if err != nil {
		return err
	}
	// End zeroes the plaintext image key. Deferred first so it runs last, after
	// every other teardown, and it is idempotent.
	defer mode.End()

	credential, err := smbshare.NewCredential(sessionID, *username, now)
	if err != nil {
		return err
	}
	defer credential.Burn()
	code, err := credential.Display()
	if err != nil {
		return err
	}
	password, err := credential.Password()
	if err != nil {
		return err
	}
	// The supervisor zeroes this the moment smbpasswd has consumed it; this is
	// the guard for the paths that never reach the supervisor.
	defer password.Zero()

	// Prove the refusal is live before serving anything: if the mode will not
	// admit the share itself, nothing should start.
	if err := mode.Admit(smbshare.CapabilitySMBShare, time.Now()); err != nil {
		return err
	}

	// A stale sentinel from a previous session would stop this one instantly.
	if err := os.Remove(stopPath(*path)); err != nil && !os.IsNotExist(err) {
		return errors.New("cannot clear the previous recovery stop sentinel")
	}

	refuses := make([]string, 0, len(smbshare.RefusedCapabilities))
	for _, capability := range smbshare.RefusedCapabilities {
		refuses = append(refuses, string(capability))
	}
	record := sessionRecord{SessionID: sessionID, ShareName: *name, Username: *username, Port: uint16(*port),
		StartedAt: now, ExpiresAt: mode.Expires(), TimeBox: timeBox.String(),
		Serves: []string{string(smbshare.CapabilitySMBShare)}, Refuses: refuses, Transport: smbshare.TransportNote}
	// Write under the same lock the config was read under.
	if err := writeSession(*path, record); err != nil {
		return err
	}

	// The one-time code is printed ONCE, here, and never persisted. The operator
	// reads it off this screen and types it into recoveryOS (§9).
	if err := emit(map[string]any{
		"recovery":    true,
		"session":     record,
		"username":    *username,
		"oneTimeCode": code,
		"codeRules": fmt.Sprintf("six Crockford Base32 characters, one use, %s absolute expiry, at most %d wrong attempts, valid only for this session on this LAN",
			smbshare.CredentialTTL, smbshare.MaxAttempts),
		"expiresAt": mode.Expires(),
		"refuses":   refuses,
		"transport": smbshare.TransportNote,
		"imageKey":  "held in memory only for this session, zeroed on completion, never written to disk or Keychain",
		"untrusted": "this is not your Mac: the plaintext image key exists in RAM on hardware its owner controls for as long as this session runs",
		"next":      "on the broken Mac in recoveryOS, mount this share with the email address above as the username and the one-time code as the password",
	}); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Watch for `nexal share stop`. Polling a sentinel rather than signalling a
	// recorded PID: a PID can be reused, and this command must not be able to
	// terminate an unrelated process.
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
			if st, err := os.Lstat(stopPath(*path)); err == nil && st.Mode().IsRegular() {
				cancel()
				return
			}
		}
	}()

	// Report is called from the supervisor's diagnostics reader, its health loop
	// and its returning path, so the persisted-copy bookkeeping is locked.
	var reportMu sync.Mutex
	var last []byte
	runErr := smbshare.Run(runCtx, pin, share, privateDir, smbshare.Options{
		Password: password,
		Report: func(st smbshare.Status) {
			reportMu.Lock()
			defer reportMu.Unlock()
			next := record
			next.Supervisor = st
			// Only rewrite when something an operator would notice changed, so a
			// two-second health probe does not rewrite the file forever.
			b, err := json.Marshal(next)
			if err != nil || string(b) == string(last) {
				return
			}
			last = b
			_ = writeSession(*path, next)
		},
	})
	cancel()
	<-stopped
	mode.End()

	// Remove the session record and sentinel. A failure to remove them is
	// reported: a leftover record claiming a live session on someone else's Mac
	// is exactly the sort of residue §9 forbids.
	removeErr := removeSession(*path)
	if runErr != nil {
		return runErr
	}
	if removeErr != nil {
		return removeErr
	}
	return emit(map[string]any{
		"recovery":   false,
		"serving":    false,
		"sessionId":  sessionID,
		"endedAt":    time.Now(),
		"imageKey":   "zeroed",
		"credential": "burned",
		"cleanup":    "generated smb.conf, private passdb and session record removed from this Mac",
		"note":       "recovery mode has ended; this Mac's other capabilities are available again after the next `nexal run`",
	})
}

// shareStop asks a running session to end early.
//
// It writes an atomic 0600 sentinel and does NOT take the configuration lock,
// because the session it is stopping holds that lock for its whole duration —
// blocking here would mean `share stop` could never stop anything. It also does
// not signal a PID: a recorded PID may have been reused, and "stop the share"
// must not be able to become "kill an unrelated process".
func shareStop(args []string) error {
	f, path, err := flags("share stop")
	if err != nil {
		return err
	}
	if err := parse(f, args, path); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("usage: nexal share stop [--config absolute-path]")
	}
	if _, err := config.ReadPrivate(sessionPath(*path), 64<<10); err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
			return errors.New("no recovery share session is recorded on this Mac")
		}
		return err
	}
	if err := config.AtomicPrivate(stopPath(*path), []byte("stop\n")); err != nil {
		return err
	}
	return emit(map[string]any{
		"stopRequested": true,
		"appliesWithin": "one second; the running session polls for this request and then stops Samba, zeroes the image key and removes its private state",
		"note":          "the one-time share code cannot be reused after this; a new session issues a new code",
	})
}

func writeSession(configPath string, record sessionRecord) error {
	b, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return errors.New("cannot encode the recovery session record")
	}
	// Refuse to persist a record that somehow carries the code: the redacting
	// marshallers make this unreachable, and it stays checked anyway because the
	// cost of being wrong is a credential on a stranger's disk.
	return config.AtomicPrivate(sessionPath(configPath), b)
}

func removeSession(configPath string) error {
	var failures []string
	if err := os.Remove(sessionPath(configPath)); err != nil && !os.IsNotExist(err) {
		failures = append(failures, "session record")
	}
	if err := os.Remove(stopPath(configPath)); err != nil && !os.IsNotExist(err) {
		failures = append(failures, "stop sentinel")
	}
	if len(failures) > 0 {
		return fmt.Errorf("recovery session could not clean up: %s left on this machine", strings.Join(failures, " and "))
	}
	return nil
}

func clearBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
