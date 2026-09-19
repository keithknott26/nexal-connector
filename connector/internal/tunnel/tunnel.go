// Package tunnel supervises one operator-provisioned cloudflared process.
// There is no installer, privileged launch, or fallback transport.
package tunnel

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"nexal/connector/internal/config"
)

type Evidence struct {
	Configured           bool      `json:"configured"`
	Connected            bool      `json:"connected"`
	ObservedProtocol     string    `json:"observedProtocol,omitempty"`
	ObservedKeyAgreement string    `json:"observedKeyAgreement,omitempty"`
	Verified             bool      `json:"verified"`
	Attestation          bool      `json:"attestation"`
	Quarantined          bool      `json:"quarantined"`
	Version              string    `json:"version"`
	Architecture         string    `json:"architecture"`
	SHA256               string    `json:"sha256"`
	SourceURL            string    `json:"sourceUrl"`
	VerificationMethod   string    `json:"verificationMethod"`
	LastObservation      time.Time `json:"lastObservation,omitempty"`
	VerificationGap      string    `json:"verificationGap"`
	Policy               string    `json:"policy"`
}

var versionRE = regexp.MustCompile(`^[0-9]{4}\.[0-9]{1,2}\.[0-9]{1,3}$`)
var hostnameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

func Validate(t config.Tunnel, listen string) error {
	if err := config.ValidateListen(listen); err != nil {
		return err
	}
	if !filepath.IsAbs(t.Binary) || !filepath.IsAbs(t.TokenFile) {
		return errors.New("cloudflared binary and token-file paths must be absolute")
	}
	digest, err := hex.DecodeString(t.SHA256)
	if err != nil || len(digest) != 32 {
		return errors.New("pinned binary SHA256 required")
	}
	if !versionRE.MatchString(t.Version) {
		return errors.New("exact tested cloudflared release version required")
	}
	if t.Architecture != runtime.GOARCH {
		return errors.New("pinned cloudflared architecture does not match this host")
	}
	u, err := url.Parse(t.SourceURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, "/cloudflare/cloudflared/releases/download/"+t.Version+"/") || strings.Contains(u.Path, "..") || u.RawPath != "" {
		return errors.New("official Cloudflare release artifact source URL required")
	}
	if strings.TrimSpace(t.VerificationMethod) == "" || len(t.VerificationMethod) > 512 {
		return errors.New("record provenance verification method and any missing publisher evidence")
	}
	if !hostnameRE.MatchString(t.Hostname) || !strings.Contains(t.Hostname, ".") || strings.Contains(t.Hostname, "..") {
		return errors.New("explicit tunnel hostname required")
	}
	st, err := os.Lstat(t.Binary)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0111 == 0 || st.Size() > 256<<20 {
		return errors.New("pinned binary must be regular, executable, bounded and not group/world writable")
	}
	f, err := os.Open(t.Binary)
	if err != nil {
		return errors.New("cannot read pinned binary")
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) || !actual.Mode().IsRegular() ||
		actual.Mode().Perm()&0022 != 0 || actual.Mode().Perm()&0111 == 0 || actual.Size() > 256<<20 {
		return errors.New("pinned binary changed during open")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(f, (256<<20)+1)); err != nil {
		return errors.New("cannot hash pinned binary")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), t.SHA256) {
		return errors.New("cloudflared integrity check failed")
	}
	token, err := config.ReadPrivate(t.TokenFile, 8192)
	if err != nil {
		return errors.New("cloudflared requires a private regular 0600 token file")
	}
	token = []byte(strings.TrimSpace(string(token)))
	if len(token) < 32 || strings.ContainsAny(string(token), " \t\r\n") {
		return errors.New("invalid cloudflared token file")
	}
	return nil
}

// BuildArgs is the only launch policy. No arbitrary options or environment
// values can replace QUIC, strict PQ, token-file or the generated config.
func BuildArgs(configPath, tokenFile string) ([]string, error) {
	if !filepath.IsAbs(configPath) || !filepath.IsAbs(tokenFile) {
		return nil, errors.New("absolute private configuration paths required")
	}
	return []string{"tunnel", "--config", configPath, "--no-autoupdate", "--protocol", "quic", "--logformat", "json", "run", "--post-quantum", "--token-file", tokenFile}, nil
}
func CleanEnv() []string {
	// Allow no TUNNEL_*, QUIC_*, proxy, LD_*, DYLD_*, config or token overrides.
	// No inherited HOME prevents fallback to an ambient cloudflared config.
	return []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
}
func writeEffectiveConfig(path string, t config.Tunnel, listen string) error {
	// JSON is valid YAML and can be parsed by cloudflared's config loader.
	v := map[string]any{
		"protocol": "quic", "post-quantum": true, "no-autoupdate": true,
		"ingress": []any{
			map[string]string{"hostname": t.Hostname, "service": "http://" + listen},
			map[string]string{"service": "http_status:404"},
		},
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return config.AtomicPrivate(path, b)
}

func Check(ctx context.Context, t config.Tunnel, listen string) (Evidence, error) {
	e := Evidence{Version: t.Version, Architecture: t.Architecture, SHA256: t.SHA256, SourceURL: t.SourceURL,
		VerificationMethod: t.VerificationMethod, Policy: "strict post-quantum key agreement: Mac-to-Cloudflare tunnel only",
		VerificationGap: "No independent deployment verification, Access/grant dispatch integration, replica inventory or supported live key-agreement attestation. Outbound pull is a separate non-PQ-verified path."}
	if err := Validate(t, listen); err != nil {
		return e, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.Binary, "--version")
	cmd.WaitDelay = time.Second
	cmd.Env = CleanEnv()
	b := &limitWriter{limit: 8192}
	cmd.Stdout = b
	if err := cmd.Run(); err != nil {
		return e, errors.New("pinned cloudflared version check failed")
	}
	// A digest alone is not evidence of publisher identity. Both the supplied
	// official artifact record and exact running version remain visible.
	if !strings.HasPrefix(strings.TrimSpace(string(b.data)), "cloudflared version "+t.Version+" ") &&
		strings.TrimSpace(string(b.data)) != "cloudflared version "+t.Version {
		return e, errors.New("cloudflared version differs from pin")
	}
	e.Configured = true
	return e, nil
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

// Observe reads only bounded structured diagnostics. A QUIC connection is not
// proof of PQ negotiation. Even a reported hybrid group is just local evidence,
// never remote attestation or eligibility for production dispatch.
func Observe(e Evidence, line []byte, now time.Time) Evidence {
	// No input, including invalid diagnostics or an inherited Evidence value,
	// can promote local observations to deployment verification/attestation.
	e.Verified = false
	e.Attestation = false
	if e.Quarantined {
		e.Connected = false
	}
	if len(line) > 64<<10 {
		e.Quarantined = true
		e.Connected = false
		return e
	}
	var event map[string]json.RawMessage
	if config.CheckJSONObject(line) != nil {
		e.Quarantined = true
		e.Connected = false
		return e
	}
	if json.Unmarshal(line, &event) != nil {
		return e
	}
	field := func(k string) string { var s string; _ = json.Unmarshal(event[k], &s); return s }
	message := strings.ToLower(field("message"))
	protocol := strings.ToLower(field("protocol"))
	var protocolValue string
	rawProtocol, hasProtocol := event["protocol"]
	invalidProtocol := hasProtocol && (json.Unmarshal(rawProtocol, &protocolValue) != nil || protocol == "")
	if invalidProtocol || (protocol != "" && protocol != "quic") || strings.Contains(message, "switching to http2") ||
		strings.Contains(message, "fallback to http2") || strings.Contains(message, "post-quantum disabled") ||
		strings.Contains(message, "post quantum disabled") {
		e.Quarantined = true
		e.Connected = false
		// Diagnostics may contain tokens or paths. Only fixed protocol names
		// may be copied into user-visible evidence.
		e.ObservedProtocol = ""
		if protocol == "http2" || protocol == "http/2" {
			e.ObservedProtocol = "http2"
		}
		e.LastObservation = now
		return e
	}
	if strings.Contains(message, "registered tunnel connection") && protocol == "quic" && !e.Quarantined {
		e.Connected = true
		e.ObservedProtocol = "quic"
		e.LastObservation = now
	}
	for _, k := range []string{"curve", "keyAgreement", "key_agreement"} {
		switch field(k) {
		case "":
			// A non-string explicit group cannot be interpreted safely.
			if raw, exists := event[k]; !exists || string(raw) == `""` {
				continue
			}
			fallthrough
		default:
			e.Quarantined = true
			e.Connected = false
			e.LastObservation = now
		case "X25519MLKEM768", "X25519Kyber768Draft00":
			e.ObservedKeyAgreement = field(k)
			e.LastObservation = now
		}
	}
	if strings.Contains(message, "unregistered tunnel connection") || strings.Contains(message, "connection terminated") {
		// Conservatively clear health even if another connector might remain.
		e.Connected = false
		e.LastObservation = now
	}
	e.Verified = false
	e.Attestation = false
	return e
}

// Run validates on every start, executes one strict process, and fails closed
// on exit, unsupported policy, or suspicious diagnostics. No fallback/retry
// binary, transport, direct route, automatic install or updater exists.
func Run(ctx context.Context, t config.Tunnel, listen, privateDir string, report func(Evidence)) error {
	e, err := Check(ctx, t, listen)
	if err != nil {
		return err
	}
	effective := filepath.Join(privateDir, "cloudflared-effective.json")
	if err = writeEffectiveConfig(effective, t, listen); err != nil {
		return err
	}
	args, err := BuildArgs(effective, t.TokenFile)
	if err != nil {
		return err
	}
	// Rehash immediately before launch rather than trusting a prior check.
	if err = Validate(t, listen); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, t.Binary, args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = CleanEnv()
	// Both streams feed a bounded line reader; raw logs (which may contain
	// sensitive diagnostics) are never echoed to terminal or persisted.
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	if err = cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return errors.New("strict cloudflared process could not start")
	}
	if report != nil {
		report(e)
	}
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		scan := bufio.NewScanner(reader)
		scan.Buffer(make([]byte, 4096), 64<<10)
		for scan.Scan() {
			mu.Lock()
			e = Observe(e, scan.Bytes(), time.Now())
			snapshot := e
			mu.Unlock()
			if report != nil {
				report(snapshot)
			}
			if snapshot.Quarantined {
				cancel()
			}
		}
		if scan.Err() != nil {
			mu.Lock()
			e.Quarantined = true
			e.Connected = false
			mu.Unlock()
			cancel()
		}
		_ = reader.Close()
	}()
	err = cmd.Wait()
	_ = writer.Close()
	<-done
	mu.Lock()
	e.Connected = false
	snapshot := e
	mu.Unlock()
	if report != nil {
		report(snapshot)
	}
	if snapshot.Quarantined {
		return errors.New("tunnel policy violation: connector quarantined; no downgrade permitted")
	}
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return errors.New("strict tunnel unavailable; no downgrade permitted")
	}
	return fmt.Errorf("strict tunnel exited; no automatic fallback")
}
