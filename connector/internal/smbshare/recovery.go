package smbshare

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Capability is a thing the connector can do. Recovery mode admits exactly one
// of them.
//
// WHY THIS TYPE EXISTS: HARDENING-PLAN §12 decides recovery is "a distinct,
// time-boxed state that serves only the SMB share and refuses every other
// capability (jobs, pool, pager)", failing closed "the way admitLocked already
// does". A comment saying so is not a refusal, so the refusal is a function with
// a test. Every capability except the share is named here so that adding a new
// one to the product does not silently add it to recovery mode: an unrecognised
// capability is refused too.
type Capability string

const (
	// CapabilitySMBShare is the ONLY capability recovery mode admits.
	CapabilitySMBShare Capability = "smb-share"
	// Everything below is refused while in recovery mode.
	CapabilityJobs           Capability = "jobs"
	CapabilityPool           Capability = "pool"
	CapabilityPager          Capability = "pager"
	CapabilityMarketplace    Capability = "marketplace"
	CapabilityTunnel         Capability = "tunnel"
	CapabilityDiscovery      Capability = "discovery"
	CapabilityBundleTransfer Capability = "bundle-transfer"
	CapabilityContribution   Capability = "contribution"
)

// RefusedCapabilities is the published list, used by the CLI to tell an operator
// what a recovery session costs them and by the tests to prove each one is
// refused. It is not the enforcement — Admit is — but it must stay in step, and
// a test asserts that it does.
var RefusedCapabilities = []Capability{
	CapabilityJobs, CapabilityPool, CapabilityPager, CapabilityMarketplace,
	CapabilityTunnel, CapabilityDiscovery, CapabilityBundleTransfer, CapabilityContribution,
}

// ErrRecoveryMode is what every refused capability returns, so a caller can test
// for "refused because this Mac is in recovery mode" rather than string-match.
var ErrRecoveryMode = errors.New("this Mac is in recovery mode and serves only the recovery SMB share")

// ErrRecoveryOver is returned once the time box has elapsed or the session has
// been ended. It is distinct from ErrRecoveryMode because the remedy differs:
// one means "wait", the other means "start a new, authorized session".
var ErrRecoveryOver = errors.New("the recovery session has ended; start a new authorized session")

// Mode is one time-boxed recovery session.
//
// It owns the plaintext image key for the life of the session and nothing else
// does. The key is held in a []byte that is zeroed on End, on expiry, and on any
// refusal to continue — never converted to a string (Go strings cannot be
// zeroed), never marshalled, never written to disk or the Keychain (§9: "The
// friend's Mac is untrusted ... Never write it to disk or Keychain, zero it on
// completion, hard-expire the session, and refuse to start if cleanup cannot be
// guaranteed").
type Mode struct {
	mu       sync.Mutex
	started  time.Time
	expires  time.Time
	ended    bool
	key      []byte
	username string
	share    string
}

// Begin starts a recovery session.
//
// It refuses to start unless cleanup can be guaranteed: the private directory
// that will hold the generated smb.conf and the passdb must be creatable AND
// removable right now, because a session that cannot erase itself must not begin
// on hardware somebody else owns.
//
// Begin TAKES OWNERSHIP of imageKey and zeroes the caller's slice, so there is
// exactly one copy of the plaintext key in the process.
func Begin(privateDir, username, shareName string, timeBox time.Duration, now time.Time, imageKey []byte) (*Mode, error) {
	// Zero the caller's copy on every path out, including refusals: a refused
	// session must not leave a plaintext key lying in the caller's buffer.
	defer zero(imageKey)
	if timeBox < MinTimeBox || timeBox > MaxTimeBox {
		return nil, fmt.Errorf("recovery time box must be between %s and %s", MinTimeBox, MaxTimeBox)
	}
	if now.IsZero() {
		return nil, errors.New("recovery session requires a wall-clock start time")
	}
	if !emailRE.MatchString(username) {
		return nil, errors.New("recovery session username must be the owner's email address")
	}
	if !shareNameRE.MatchString(shareName) {
		return nil, errors.New("recovery session requires a valid share name")
	}
	// AES-256-XTS/GCM key material. A short or all-zero key is a bug upstream,
	// and starting a restore with one wastes hours before failing.
	if len(imageKey) != 32 {
		return nil, errors.New("recovery session requires a 32-byte plaintext image key held in memory only")
	}
	var any byte
	for _, b := range imageKey {
		any |= b
	}
	if any == 0 {
		return nil, errors.New("refusing an all-zero image key")
	}
	if err := preflightCleanup(privateDir); err != nil {
		return nil, err
	}
	owned := make([]byte, len(imageKey))
	copy(owned, imageKey)
	return &Mode{started: now, expires: now.Add(timeBox), key: owned, username: username, share: shareName}, nil
}

// Admit is the refusal. It is the only way to ask "may I do this while in
// recovery mode", and it answers no to everything except the share.
//
// FAIL CLOSED, like agent.admitLocked: an expired session, an ended session, a
// zero clock or an unrecognised capability are all refusals. There is no
// argument that produces a yes other than CapabilitySMBShare inside the time
// box.
func (m *Mode) Admit(c Capability, now time.Time) error {
	if m == nil {
		return ErrRecoveryOver
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ended {
		return ErrRecoveryOver
	}
	// A caller that cannot say what time it is cannot be told the session is
	// still live.
	if now.IsZero() {
		return ErrRecoveryOver
	}
	if !now.Before(m.expires) {
		// Expiry is enforced here rather than by a timer, so an expired session
		// is refused even if no goroutine was running to notice.
		m.endLocked()
		return ErrRecoveryOver
	}
	if c != CapabilitySMBShare {
		return fmt.Errorf("%w: %q is refused until the session ends at %s",
			ErrRecoveryMode, string(c), m.expires.UTC().Format(time.RFC3339))
	}
	return nil
}

// Expires reports the hard deadline. Read-only: there is no Extend, on purpose.
func (m *Mode) Expires() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.expires
}

// Active reports whether the session is still live at now.
func (m *Mode) Active(now time.Time) bool {
	return m != nil && m.Admit(CapabilitySMBShare, now) == nil
}

// UseImageKey lends the plaintext key for the duration of one call. The slice is
// valid only inside f and must not be retained; f must not copy it anywhere that
// outlives the session, and nothing in this package does.
func (m *Mode) UseImageKey(f func([]byte) error) error {
	if m == nil || f == nil {
		return ErrRecoveryOver
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ended || m.key == nil {
		return ErrRecoveryOver
	}
	return f(m.key)
}

// End hard-expires the session and zeroes the plaintext key. It is idempotent so
// a deferred End and an explicit End cannot double-free or panic.
func (m *Mode) End() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endLocked()
}

func (m *Mode) endLocked() {
	m.ended = true
	zero(m.key)
	m.key = nil
}

// KeyZeroed exists for the test that proves the plaintext key does not survive
// the session. It reports whether the key material is gone.
func (m *Mode) KeyZeroed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.key == nil
}

// MarshalJSON guarantees a Mode cannot leak key material through a status
// payload, a log record or a UI response, no matter who embeds it.
func (m *Mode) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte(`{"recovery":false}`), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return json.Marshal(map[string]any{
		"recovery":  true,
		"active":    !m.ended,
		"startedAt": m.started,
		"expiresAt": m.expires,
		"share":     m.share,
		"username":  m.username,
		"serves":    []string{string(CapabilitySMBShare)},
		"refuses":   RefusedCapabilities,
		"imageKey":  "held in memory only for this session; never written to disk or Keychain and zeroed on completion",
		"transport": TransportNote,
	})
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// Password is a zeroable plaintext credential.
//
// HONEST LIMIT: reveal returns a Go string because os/exec's stdin wants one and
// Go strings cannot be zeroed, so a copy of the share password may persist in
// unreachable heap until collection. That is accepted for the SHARE PASSWORD,
// which is one-use, 60-minute, single-session and single-LAN. It is NOT accepted
// for the image key, which is why Mode.key is []byte from end to end and never
// becomes a string.
type Password struct {
	mu     sync.Mutex
	b      []byte
	zeroed bool
}

func NewPassword(b []byte) *Password {
	owned := make([]byte, len(b))
	copy(owned, b)
	return &Password{b: owned}
}

// Usable reports whether the credential is still present and plausible.
func (p *Password) Usable() error {
	if p == nil {
		return errors.New("no share credential")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.zeroed || len(p.b) == 0 {
		return errors.New("share credential has already been consumed and zeroed")
	}
	if len(p.b) < CodeLength {
		return errors.New("share credential is too short")
	}
	return nil
}

func (p *Password) reveal() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.zeroed {
		return ""
	}
	return string(p.b)
}

// Zero erases the credential. Idempotent.
func (p *Password) Zero() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	zero(p.b)
	p.b = nil
	p.zeroed = true
}

// Zeroed reports whether Zero has run. Used by tests to prove provisioning
// consumes the credential.
func (p *Password) Zeroed() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.zeroed
}

// MarshalJSON keeps a Password out of any payload that embeds it.
func (p *Password) MarshalJSON() ([]byte, error) {
	return []byte(`"redacted: one-time share credential is displayed once and never serialized"`), nil
}

const (
	// CodeLength and codeAlphabet are §13's decision: six characters of Crockford
	// Base32, whose alphabet drops I, L, O and U so a code survives being read
	// off one Mac's screen and typed into recoveryOS on another.
	CodeLength   = 6
	codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	// CredentialTTL is §13's 60-minute ABSOLUTE ttl. Not sliding: a code does not
	// live longer because someone kept trying it.
	CredentialTTL = time.Hour
	// MaxAttempts is what makes 30 bits sufficient (§13): five wrong guesses in a
	// space of about a billion, then the code is burned and a new one must be
	// issued. Without this the code length would be indefensible.
	MaxAttempts = 5
)

// Credential is the one-time SMB share password, bound to one recovery session.
//
// BOUND, NEVER BEARER (§13). Verify requires the session identifier the code was
// issued for, so a guessed or intercepted code is useless to anyone who is not
// already that session. The code is also separate from the image encryption key
// (§9) — knowing it gets you a share, not a readable backup.
type Credential struct {
	mu        sync.Mutex
	sessionID string
	username  string
	code      []byte
	issued    time.Time
	expires   time.Time
	attempts  int
	consumed  bool
	burned    bool
	taken     bool
	displayed bool
}

// NewCredential mints a code for one session. The code is generated with
// crypto/rand and rejection sampling, so the 32-character alphabet stays uniform
// rather than biased toward its first 24 symbols the way modulo would make it.
func NewCredential(sessionID, username string, now time.Time) (*Credential, error) {
	if strings.TrimSpace(sessionID) == "" || len(sessionID) > 128 {
		return nil, errors.New("a share credential must be bound to a recovery session identifier; a bearer code is refused")
	}
	if !emailRE.MatchString(username) {
		return nil, errors.New("the SMB username must be the owner's email address")
	}
	if now.IsZero() {
		return nil, errors.New("a share credential requires a wall-clock issue time")
	}
	code := make([]byte, 0, CodeLength)
	buf := make([]byte, 1)
	for len(code) < CodeLength {
		if _, err := rand.Read(buf); err != nil {
			zero(code)
			return nil, errors.New("cannot generate a share credential")
		}
		// Rejection sampling: 256 is not a multiple of 32 only if the alphabet
		// changes size, so this also protects a future alphabet edit.
		if int(buf[0]) >= (256/len(codeAlphabet))*len(codeAlphabet) {
			continue
		}
		code = append(code, codeAlphabet[int(buf[0])%len(codeAlphabet)])
	}
	return &Credential{sessionID: sessionID, username: username, code: code,
		issued: now, expires: now.Add(CredentialTTL)}, nil
}

// Display returns the code for the ONE screen that shows it to the user, and
// refuses a second call. Recovery asks the user to read six characters off the
// friend's Mac; a code that can be re-displayed is a code that can be recovered
// by whoever walks past that Mac later.
func (c *Credential) Display() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.liveLocked(); err != nil {
		return "", err
	}
	if c.displayed {
		return "", errors.New("this one-time code has already been displayed; end the session and issue a new one")
	}
	c.displayed = true
	return string(c.code), nil
}

// Password hands the plaintext to the provisioning step exactly once. The
// returned Password is zeroed by provision(), so this cannot be used to keep a
// copy alive past the share coming up.
func (c *Credential) Password() (*Password, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.liveLocked(); err != nil {
		return nil, err
	}
	if c.taken {
		return nil, errors.New("the share credential has already been provisioned once")
	}
	c.taken = true
	return NewPassword(c.code), nil
}

// Verify consumes the code. It is one-use with atomic consumption (§13), bound to
// the session, absolute-TTL, and burned after MaxAttempts wrong guesses.
func (c *Credential) Verify(sessionID, code string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.liveLocked(); err != nil {
		return err
	}
	if now.IsZero() || !now.Before(c.expires) {
		c.burnLocked()
		return errors.New("this one-time code has expired")
	}
	// The binding is checked BEFORE the attempt is counted, so a wrong session
	// cannot burn a live code belonging to the right one.
	if subtle.ConstantTimeCompare([]byte(sessionID), []byte(c.sessionID)) != 1 {
		return errors.New("this code was not issued to this recovery session")
	}
	normalized := NormalizeCode(code)
	if len(normalized) != CodeLength {
		c.attempts++
		if c.attempts >= MaxAttempts {
			c.burnLocked()
			return errors.New("too many wrong attempts; this code is burned and a new one must be issued")
		}
		return errors.New("that code is not valid")
	}
	if subtle.ConstantTimeCompare([]byte(normalized), c.code) != 1 {
		c.attempts++
		if c.attempts >= MaxAttempts {
			c.burnLocked()
			return errors.New("too many wrong attempts; this code is burned and a new one must be issued")
		}
		return errors.New("that code is not valid")
	}
	// Consumed atomically under the same lock that checked it: two concurrent
	// verifications cannot both succeed.
	c.consumed = true
	zero(c.code)
	c.code = nil
	return nil
}

func (c *Credential) liveLocked() error {
	switch {
	case c.burned:
		return errors.New("this one-time code is burned; issue a new one")
	case c.consumed:
		return errors.New("this one-time code has already been used")
	case c.code == nil:
		return errors.New("this one-time code is no longer available")
	}
	return nil
}

func (c *Credential) burnLocked() {
	c.burned = true
	zero(c.code)
	c.code = nil
}

// Burn discards the code unconditionally, for the session-teardown path.
func (c *Credential) Burn() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.burnLocked()
}

// ExpiresAt and Username are the nonsecret facts a status payload may show.
func (c *Credential) ExpiresAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.expires
}

func (c *Credential) Username() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.username
}

// MarshalJSON keeps the code out of any payload that embeds a Credential.
func (c *Credential) MarshalJSON() ([]byte, error) {
	if c == nil {
		return []byte("null"), nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return json.Marshal(map[string]any{
		"username":    c.username,
		"issuedAt":    c.issued,
		"expiresAt":   c.expires,
		"oneTime":     true,
		"bound":       true,
		"maxAttempts": MaxAttempts,
		"code":        "displayed once on this Mac's screen; never logged, never serialized, never sent anywhere",
	})
}

// NormalizeCode applies Crockford Base32's read-aloud rules: case folds, maps the
// characters humans substitute (I and l for 1, O for 0), and drops the separators
// people add when copying six characters off a screen. Anything else is left in
// place so it fails the comparison rather than being silently corrected.
func NormalizeCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		switch r {
		case '-', ' ', '\t':
			continue
		case 'I', 'L':
			b.WriteByte('1')
		case 'O':
			b.WriteByte('0')
		case 'U':
			// U is excluded from the alphabet; mapping it to V would invent a
			// character the issuer never generated, so it is kept and fails.
			b.WriteByte('U')
		default:
			if r > 127 {
				// Non-ASCII cannot be a code character; keep one byte so the
				// length check still rejects it.
				b.WriteByte('?')
				continue
			}
			b.WriteByte(byte(r))
		}
	}
	return b.String()
}
