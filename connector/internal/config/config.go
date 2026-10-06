// Package config stores nonsecret policy separately from credentials.
package config

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const Version = "0.4.1"

type Tunnel struct {
	Binary             string `json:"binary"`
	SHA256             string `json:"sha256"`
	Version            string `json:"version"`
	Architecture       string `json:"architecture"`
	SourceURL          string `json:"sourceUrl"`
	VerificationMethod string `json:"verificationMethod"`
	TokenFile          string `json:"tokenFile"`
	Hostname           string `json:"hostname"`
}

type Config struct {
	Version            int    `json:"version"`
	Coordinator        string `json:"coordinator"`
	Name               string `json:"name"`
	HostID             string `json:"hostId,omitempty"`
	Listen             string `json:"listen"`
	Development        bool   `json:"development"`
	DevSecrets         bool   `json:"devSecrets"`
	Paused             bool   `json:"paused"`
	MarketplaceEnabled bool   `json:"marketplaceEnabled"`
	MemoryLimitBytes   uint64 `json:"memoryLimitBytes"`
	ReserveMemoryBytes uint64 `json:"reserveMemoryBytes"`
	IdleSeconds        uint64 `json:"idleSeconds"`
	// ShareWhileActive is the owner's standing choice to take work even while
	// they are using this Mac. Memory, disk, battery and thermal limits still
	// apply; only the "owner is active" gate is lifted. On by default: a saved
	// configuration without the field loads as on (see Load), and it is always
	// written explicitly so turning it off sticks.
	ShareWhileActive bool `json:"shareWhileActive"`
	// Upload throttling (HARDENING-PLAN §16, rate half only). Absent means auto:
	// these fields are omitempty so a config written before they existed is not
	// rewritten with empty strings, and NormalizeUploadMode reads the absence as
	// the shaped default rather than as unlimited. Nothing enforces them on bulk
	// data yet because no bulk upload path exists — see internal/throttle.
	UploadMode                   string `json:"uploadMode,omitempty"`
	UploadLimitBytesPerSecond    uint64 `json:"uploadLimitBytesPerSecond,omitempty"`
	MeasuredUploadBytesPerSecond uint64 `json:"measuredUploadBytesPerSecond,omitempty"`
	// MinFreeDiskBytes is the owner's disk reserve for HARDENING-PLAN §36.4's
	// "pause when disk is low" condition — the disk analogue of
	// ReserveMemoryBytes. Absent/0 means the reviewed default
	// (contribution.MinFreeDiskBytesDefault), never "no floor": a zero reserve
	// would mean contributing until the owner's volume is full. omitempty for the
	// same reason as the upload fields — a config written before this existed is
	// not rewritten, and silence reads as the safe default.
	MinFreeDiskBytes uint64  `json:"minFreeDiskBytes,omitempty"`
	Tunnel           *Tunnel `json:"tunnel,omitempty"`
	// Discovery is absent by default, and absent means off. Peer discovery joins
	// multicast groups and publishes this host's addresses, so it is an explicit
	// owner decision rather than something a new binary starts doing on upgrade.
	Discovery *Discovery `json:"discovery,omitempty"`
	// StaticPeers are owner-typed peer endpoints for peers mDNS cannot find —
	// principally two Macs on different VLANs, where the ROUTED TRANSPORT ALREADY
	// WORKS (an RFC1918 address on another subnet satisfies pool.privateIP) and
	// only link-local discovery does not. omitempty and absent-means-none, like
	// the fields above, so a config written before this existed is untouched and
	// an upgrade adds no peers. See staticpeers.go and TRANSPORT-NAT-DESIGN.md.
	//
	// A configured address is a dial hint, never authorization (§30.2): the
	// fingerprint is what peer TLS pins, and the coordinator's list still decides
	// AllowedPeers.
	StaticPeers []StaticPeer     `json:"staticPeers,omitempty"`
	Enrollment  *EnrollmentState `json:"enrollment,omitempty"`
	GuestAccess *GuestAccess     `json:"guestAccess,omitempty"`
	// RecoveryAuthorityPublicKey is the coordinator's public Ed25519 key in
	// unpadded base64url. It authorizes a selected helper; it is not a secret.
	// Absent means production disaster-recovery sharing is disabled.
	RecoveryAuthorityPublicKey string `json:"recoveryAuthorityPublicKey,omitempty"`
}

// EnrollmentState contains durable, non-secret identifiers only. The credential
// returned after successful pairing is stored under mesh-credential in Keychain.
type EnrollmentState struct {
	SchemaVersion int    `json:"schemaVersion"`
	SessionID     string `json:"sessionId,omitempty"`
	Status        string `json:"status"`
	AccountID     string `json:"accountId,omitempty"`
	NetworkID     string `json:"networkId,omitempty"`
	DeviceID      string `json:"deviceId,omitempty"`
	ManagementURL string `json:"managementUrl,omitempty"`
	PairedAt      string `json:"pairedAt,omitempty"`
	ExpiresAt     string `json:"expiresAt,omitempty"`
}

// Discovery configures LAN/WAN peer discovery. Every gate defaults to false and
// the zero value is the closed state (discovery.SharingPolicy has the same
// shape and the same default, and its Listen/NewRendezvous constructors refuse
// to start on a shut gate).
//
// ResourceSharing is deliberately NOT settable here. Whether offering this
// machine's CPU/GPU/RAM should default on is the unresolved product decision in
// HARDENING-PLAN §36.4, and a config field would quietly turn it into an
// implementation detail.
type Discovery struct {
	// LANDiscovery gates joining the mDNS groups at all — both announcing this
	// host on the local link and browsing for others.
	LANDiscovery bool `json:"lanDiscovery"`
	// WANRendezvous gates publishing this host's addresses to the coordinator and
	// polling its peer directory.
	WANRendezvous bool `json:"wanRendezvous"`
	// DeviceFingerprint is pool.DeviceID for this host's peer identity: 64
	// lowercase hex characters. It is a public key hash, not a secret, which is
	// why it lives in the config rather than the Keychain.
	//
	// There is no automatic generation here on purpose. pool.Identity keys are
	// deliberately not persisted by that package ("the embedding agent must use
	// its reviewed credential/key recovery policy"), so inventing a key-storage
	// scheme as a side effect of enabling discovery would be the wrong place to
	// decide it. Until a fingerprint is configured, discovery publishes nothing.
	DeviceFingerprint string `json:"deviceFingerprint,omitempty"`
	// PeerPort is the peer object-transfer port published as the dial candidate.
	PeerPort uint16 `json:"peerPort,omitempty"`
}

// Enabled reports whether any discovery mechanism is configured to run. A nil
// Discovery, or one with both gates false, means nothing starts.
func (d *Discovery) Enabled() bool {
	return d != nil && (d.LANDiscovery || d.WANRendezvous)
}

// Validate refuses a half-configured gate rather than opening it partially: a
// host that means to advertise must say which fingerprint and which port, and a
// gate that cannot be honoured is a configuration error, not a silent no-op.
func (d *Discovery) Validate() error {
	if d == nil {
		return nil
	}
	if d.DeviceFingerprint != "" && !validDeviceFingerprint(d.DeviceFingerprint) {
		return errors.New("discovery deviceFingerprint must be 64 lowercase hex characters")
	}
	if !d.Enabled() {
		return nil
	}
	if d.DeviceFingerprint == "" {
		return errors.New("discovery requires a deviceFingerprint; peers are identified by an exact key fingerprint, never by name or address")
	}
	if d.PeerPort == 0 {
		return errors.New("discovery requires a peerPort to publish as the dial candidate")
	}
	return nil
}

// validDeviceFingerprint is pool.DeviceID's exact output shape. Uppercase is
// rejected rather than folded, so the published value is byte-identical to the
// value peer TLS pins and to the coordinator's device_fingerprint_shape check.
func validDeviceFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func DefaultPath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(h, "Library", "Application Support", "Nexal", "config.json"), nil
	}
	return filepath.Join(h, ".config", "nexal", "config.json"), nil
}

// ValidateURL never allows credentials, URL ambiguities, query-based secrets, or
// plaintext except a numeric loopback IP in explicit development mode.
func ValidateURL(raw string, dev bool) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return errors.New("invalid coordinator URL")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		u.Opaque != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") ||
		strings.ContainsAny(raw, "\\\r\n\t ") {
		return errors.New("coordinator must be an origin URL without credentials, path, query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && dev && ip != nil && ip.IsLoopback() {
		return nil
	}
	if u.Scheme != "https" {
		return errors.New("HTTPS required; HTTP is allowed only for explicit development numeric loopback")
	}
	if u.Hostname() == "" {
		return errors.New("missing coordinator hostname")
	}
	return nil
}

func ValidateListen(s string) error {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return errors.New("listen must be numeric loopback host:port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("local API must bind only to numeric loopback")
	}
	p, err := net.LookupPort("tcp", port)
	if err != nil || p < 1 || p > 65535 {
		return errors.New("invalid listen port")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return errors.New("listen port must be numeric")
		}
	}
	return nil
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return errors.New("unsupported config version")
	}
	if err := ValidateURL(c.Coordinator, c.Development); err != nil {
		return err
	}
	if err := ValidateListen(c.Listen); err != nil {
		return err
	}
	if c.DevSecrets && !c.Development {
		return errors.New("file secrets are forbidden in production")
	}
	if c.MarketplaceEnabled {
		return errors.New("public execution is gated pending verified tunnel dispatch")
	}
	if len(strings.TrimSpace(c.Name)) < 1 || len(c.Name) > 80 {
		return errors.New("host name must contain 1–80 bytes and not be blank")
	}
	if err := c.Discovery.Validate(); err != nil {
		return err
	}
	if err := ValidateStaticPeers(c.StaticPeers); err != nil {
		return err
	}
	if c.Enrollment != nil {
		if c.Enrollment.SchemaVersion != 2 {
			return errors.New("unsupported enrollment state")
		}
		switch c.Enrollment.Status {
		case "provisioning", "joining", "paired", "revoked":
		default:
			return errors.New("invalid enrollment state")
		}
		if (c.Enrollment.Status == "joining" || c.Enrollment.Status == "paired") && (c.Enrollment.AccountID == "" || c.Enrollment.NetworkID == "" || !validDeviceFingerprint(c.Enrollment.DeviceID) || c.Enrollment.PairedAt == "" || ValidateURL(c.Enrollment.ManagementURL, false) != nil) {
			return errors.New("paired enrollment state is incomplete")
		}
	}
	if c.GuestAccess != nil {
		if err := c.GuestAccess.Validate(); err != nil {
			return err
		}
	}
	return c.ResourcePolicy().Validate()
}

func Load(path string) (Config, error) {
	var c Config
	b, err := ReadPrivate(path, 64<<10)
	if err != nil {
		return c, err
	}
	if err := CheckJSONObject(b); err != nil {
		return c, errors.New("invalid configuration JSON")
	}
	// Null scalar values otherwise silently become Go zero values, including
	// paused=false. An omitted pause decision must never imply owner consent.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return c, errors.New("invalid configuration JSON")
	}
	hasPause, hasShareWhileActive := false, false
	for key, value := range fields {
		if strings.EqualFold(key, "paused") {
			hasPause = true
		}
		if strings.EqualFold(key, "shareWhileActive") {
			hasShareWhileActive = true
		}
		if strings.EqualFold(key, "tunnel") || strings.EqualFold(key, "discovery") || strings.EqualFold(key, "enrollment") {
			continue
		}
		if strings.TrimSpace(string(value)) == "null" {
			return c, errors.New("null configuration field")
		}
	}
	if !hasPause {
		return c, errors.New("explicit saved pause policy required")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, errors.New("invalid configuration JSON")
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return c, errors.New("trailing configuration data")
	}
	if !hasShareWhileActive {
		c.ShareWhileActive = true // the default until the owner turns it off
	}
	return c, c.Validate()
}

func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return AtomicPrivate(path, append(b, '\n'))
}

// AtomicPrivate writes to a private temporary file, fsyncs, and atomically
// renames it. Existing files or leaf directories with loose modes are rejected.
func AtomicPrivate(path string, data []byte) error {
	if !filepath.IsAbs(path) {
		return errors.New("private path must be absolute")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("cannot create private directory")
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return errors.New("private directory must have mode 0700 and not be a symlink")
	}
	if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0) {
		return errors.New("private file must be regular and mode 0600")
	} else if err != nil && !os.IsNotExist(err) {
		return errors.New("cannot inspect private file")
	}
	f, err := os.CreateTemp(dir, ".nexal-*")
	if err != nil {
		return errors.New("cannot create private temporary file")
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errors.New("cannot write private file")
	}
	if err = os.Rename(tmp, path); err != nil {
		return errors.New("cannot atomically replace private file")
	}
	d, err := os.Open(dir)
	if err == nil {
		defer d.Close()
		_ = d.Sync()
	}
	return nil
}

func ReadPrivate(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) || limit < 0 || limit == 1<<63-1 {
		return nil, errors.New("absolute private path and bounded limit required")
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, privateReadError{err}
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > limit {
		return nil, errors.New("private file must be regular, bounded and mode 0600")
	}
	// O_NOFOLLOW closes the leaf-symlink race. O_NONBLOCK prevents a file
	// replaced with a FIFO between inspection and open from hanging the agent.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot read private file")
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) || !actual.Mode().IsRegular() ||
		actual.Mode().Perm()&0077 != 0 || actual.Size() > limit {
		return nil, errors.New("private file changed during open")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("private file exceeds limit")
	}
	return b, nil
}

// Preserve errors.Is (especially ErrNotExist for first startup), without
// exposing the configured path or an OS diagnostic through CLI JSON errors.
type privateReadError struct{ cause error }

func (privateReadError) Error() string   { return "private file unavailable" }
func (e privateReadError) Unwrap() error { return e.cause }
