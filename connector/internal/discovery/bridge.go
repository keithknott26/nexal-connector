package discovery

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const SMBService = "_smb._tcp"
const ScreenSharingService = "_rfb._tcp"

// BridgeRecord is the authenticated multicast-over-unicast envelope. The
// transport authenticates the entire envelope; consumers still validate it to
// enforce network/site scope, replay ordering, hop count and bounded leases.
type BridgeRecord struct {
	Service   string `json:"service"`
	Instance  string `json:"instance"`
	Target    string `json:"target"`
	Port      uint16 `json:"port"`
	OriginID  string `json:"originId"`
	SiteID    string `json:"siteId"`
	NetworkID string `json:"networkId"`
	Sequence  uint64 `json:"sequence"`
	HopLimit  uint8  `json:"hopLimit"`
	TTL       uint32 `json:"ttlSeconds"`
}

func (r BridgeRecord) Validate(expectedNetwork string, lastSequence uint64) error {
	if r.Service != SMBService && r.Service != ScreenSharingService {
		return errors.New("discovery bridge service is not allowlisted")
	}
	if r.Instance == "" || len(r.Instance) > 80 || r.Target == "" || r.OriginID == "" || len(r.OriginID) > 128 || r.SiteID == "" || len(r.SiteID) > 128 || r.NetworkID == "" || len(r.NetworkID) > 128 {
		return errors.New("discovery bridge record is incomplete")
	}
	if !isNexalTarget(r.Target) {
		return errors.New("discovery target is not a private neXal hostname")
	}
	if expectedNetwork == "" || r.NetworkID != expectedNetwork {
		return errors.New("discovery bridge network scope mismatch")
	}
	expectedPort := uint16(445)
	if r.Service == ScreenSharingService {
		expectedPort = 5900
	}
	if r.Port != expectedPort {
		return errors.New("discovery record advertises an invalid service port")
	}
	if r.Sequence <= lastSequence {
		return errors.New("discovery bridge replay rejected")
	}
	if r.HopLimit == 0 || r.HopLimit > 2 {
		return errors.New("discovery bridge hop limit rejected")
	}
	if r.TTL == 0 || r.TTL > 120 {
		return errors.New("discovery bridge lease is out of bounds")
	}
	return nil
}

func isNexalTarget(target string) bool {
	return len(target) > len(".mesh.nexal.systems") && len(target) <= 253 &&
		strings.HasSuffix(strings.ToLower(target), ".mesh.nexal.systems") &&
		!strings.ContainsAny(target, "/\\@ \t\r\n")
}

func (r BridgeRecord) DedupKey() string {
	return fmt.Sprintf("%s|%s|%s|%s", r.NetworkID, r.OriginID, r.Service, r.Instance)
}

func (r BridgeRecord) ExpiresAt(observed time.Time) time.Time {
	return observed.Add(time.Duration(r.TTL) * time.Second)
}

// Gateway is an optional per-site implementation boundary. Implementations
// browse local Bonjour, sanitize records, and republish leased BridgeRecords;
// they never route 224.0.0.251 or imply a shared broadcast domain.
type Gateway interface {
	Snapshot() []BridgeRecord
}

// Receiver owns replay and dedup state for one authenticated network stream.
// Expired leases are removed before each admission.
type Receiver struct {
	NetworkID    string
	lastSequence map[string]uint64
	seen         map[string]time.Time
}

func NewReceiver(networkID string) *Receiver {
	return &Receiver{NetworkID: networkID, lastSequence: map[string]uint64{}, seen: map[string]time.Time{}}
}

func (r *Receiver) Accept(record BridgeRecord, now time.Time) error {
	if r == nil {
		return errors.New("discovery receiver unavailable")
	}
	for key, expiry := range r.seen {
		if !now.Before(expiry) {
			delete(r.seen, key)
		}
	}
	origin := record.SiteID + "|" + record.OriginID
	if err := record.Validate(r.NetworkID, r.lastSequence[origin]); err != nil {
		return err
	}
	key := record.DedupKey()
	if expiry, exists := r.seen[key]; exists && now.Before(expiry) {
		return errors.New("discovery bridge duplicate rejected")
	}
	r.lastSequence[origin] = record.Sequence
	r.seen[key] = record.ExpiresAt(now)
	return nil
}
