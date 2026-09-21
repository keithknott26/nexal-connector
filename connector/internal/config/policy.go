package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"nexal/connector/internal/contribution"
	"nexal/connector/internal/throttle"
)

// Upload throttle modes. Auto is the default because the founder request is that
// a customer should not have to know their own upstream bandwidth to avoid
// saturating it, and because an unmeasured link is the one most likely to be
// saturated. Unlimited stays available but is an explicit owner decision — it is
// never a fallback, never a default, and never what a failed measurement
// produces (see throttle.ConservativeBytesPerSecond).
const (
	UploadModeAuto      = "auto"
	UploadModeManual    = "manual"
	UploadModeUnlimited = "unlimited"
)

// ResourcePolicy intentionally excludes identity, credentials, transport and
// public execution switches. Updating resource consent cannot enable dispatch.
//
// The upload dimension is HARDENING-PLAN §16's first half: §16 requires a
// per-donor monthly bandwidth budget with cap-aware scheduling, and observes that
// "§5 has no bandwidth dimension at all". This adds the rate dimension. The
// monthly budget and cap-aware scheduling are NOT here and are not claimed.
//
// Nothing enforces these limits on bulk data yet, because no bulk upload path
// exists (internal/bundletransfer moves 6 KB enrollment bundles; the Time
// Machine / JuiceFS path is gated behind §21 Step 0). These are the consent
// values and the mechanism, ready for that path.
type ResourcePolicy struct {
	MemoryLimitBytes   uint64 `json:"memoryLimitBytes"`
	ReserveMemoryBytes uint64 `json:"reserveMemoryBytes"`
	IdleSeconds        uint64 `json:"idleSeconds"`
	// UploadMode is auto|manual|unlimited. Empty decodes and validates as auto
	// so a configuration written by an older connector keeps working; see
	// NormalizeUploadMode.
	UploadMode string `json:"uploadMode"`
	// UploadLimitBytesPerSecond is the owner's manual ceiling and is meaningful
	// only in manual mode. In auto and unlimited mode it must be zero rather
	// than a remembered-but-ignored number, matching how Discovery refuses a
	// half-configured gate instead of half-honouring it.
	UploadLimitBytesPerSecond uint64 `json:"uploadLimitBytesPerSecond"`
	// MeasuredUploadBytesPerSecond is what auto mode last derived. It is
	// DERIVED, never client-supplied: the strict decoder accepts the field for
	// round-trip symmetry with the GET response and then ignores its value (see
	// DecodeResourcePolicy). It is persisted so a restart does not begin at the
	// cold-start default on a link that was already measured, and zero means
	// "never measured".
	MeasuredUploadBytesPerSecond uint64 `json:"measuredUploadBytesPerSecond"`
	// MinFreeDiskBytes is the §36.4 disk-low floor, and it is the ONLY one of the
	// three new conditions that is owner-tunable. That asymmetry is deliberate:
	// free-space needs genuinely differ between a 256 GB MacBook Air and an 8 TB
	// Studio, whereas "do not run on battery" and "do not run while thermally
	// throttled" are §36.4 safety properties rather than preferences, and a knob
	// to switch them off would be a knob to re-create the support incident §36.4
	// exists to prevent.
	//
	// Optional, like UploadMode: 0 decodes and validates as the reviewed default
	// so a payload written before this field existed keeps working.
	MinFreeDiskBytes uint64 `json:"minFreeDiskBytes"`
}

// NormalizeUploadMode maps the absent value onto the default. Backward
// compatibility is deliberate and stated: a config file or a PUT body written
// before this field existed decodes to auto, which is the safer of the two
// possible readings of silence — the alternative, treating silence as unlimited,
// would turn an upgrade into an unshaped uplink.
func NormalizeUploadMode(mode string) string {
	if mode == "" {
		return UploadModeAuto
	}
	return mode
}

func (c Config) ResourcePolicy() ResourcePolicy {
	return ResourcePolicy{MemoryLimitBytes: c.MemoryLimitBytes,
		ReserveMemoryBytes: c.ReserveMemoryBytes, IdleSeconds: c.IdleSeconds,
		UploadMode:                   NormalizeUploadMode(c.UploadMode),
		UploadLimitBytesPerSecond:    c.UploadLimitBytesPerSecond,
		MeasuredUploadBytesPerSecond: c.MeasuredUploadBytesPerSecond,
		MinFreeDiskBytes:             c.MinFreeDiskBytes}
}

func (p ResourcePolicy) Validate() error {
	if p.MemoryLimitBytes < 64<<20 || p.MemoryLimitBytes > 8<<30 {
		return errors.New("approved memory limit must be 64 MiB–8 GiB")
	}
	if p.ReserveMemoryBytes < 128<<20 || p.ReserveMemoryBytes > 1<<40 {
		return errors.New("owner memory reserve must be 128 MiB–1 TiB")
	}
	if p.IdleSeconds < 30 || p.IdleSeconds > 86400 {
		return errors.New("idle threshold must be 30–86400 seconds")
	}
	switch NormalizeUploadMode(p.UploadMode) {
	case UploadModeManual:
		if p.UploadLimitBytesPerSecond < throttle.MinBytesPerSecond ||
			p.UploadLimitBytesPerSecond > throttle.MaxBytesPerSecond {
			return errors.New("manual upload limit must be 32 KiB/s–1 GiB/s")
		}
	case UploadModeAuto, UploadModeUnlimited:
		// A limit that no mode consults is a trap: the owner believes it applies.
		if p.UploadLimitBytesPerSecond != 0 {
			return errors.New("uploadLimitBytesPerSecond is only valid in manual mode")
		}
	default:
		return errors.New("upload mode must be auto, manual or unlimited")
	}
	if p.MeasuredUploadBytesPerSecond != 0 &&
		(p.MeasuredUploadBytesPerSecond < throttle.MinBytesPerSecond ||
			p.MeasuredUploadBytesPerSecond > throttle.MaxBytesPerSecond) {
		return errors.New("measured upload rate must be 0 or 32 KiB/s–1 GiB/s")
	}
	// 0 is "use the reviewed default", which is why it is accepted here rather
	// than clamped up: the accessor resolves it, so the stored value stays a
	// faithful record of what the owner chose (or did not choose).
	if p.MinFreeDiskBytes != 0 &&
		(p.MinFreeDiskBytes < contribution.MinFreeDiskBytesFloor ||
			p.MinFreeDiskBytes > contribution.MinFreeDiskBytesCeiling) {
		return errors.New("disk reserve must be 0 (default) or 1 GiB–1 TiB")
	}
	return nil
}

func (c Config) WithResourcePolicy(p ResourcePolicy) Config {
	c.MemoryLimitBytes, c.ReserveMemoryBytes, c.IdleSeconds =
		p.MemoryLimitBytes, p.ReserveMemoryBytes, p.IdleSeconds
	c.UploadMode = NormalizeUploadMode(p.UploadMode)
	c.UploadLimitBytesPerSecond = p.UploadLimitBytesPerSecond
	c.MeasuredUploadBytesPerSecond = p.MeasuredUploadBytesPerSecond
	c.MinFreeDiskBytes = p.MinFreeDiskBytes
	return c
}

// EffectiveMinFreeDisk resolves the §36.4 disk floor, mapping the "owner never
// chose" zero onto the reviewed default. It exists so no caller has to remember
// that zero is not a floor, which is the mistake that would turn a disk
// protection into a no-op.
func (p ResourcePolicy) EffectiveMinFreeDisk() uint64 {
	return contribution.Policy{MinFreeDiskBytes: p.MinFreeDiskBytes}.MinFreeDisk()
}

// EffectiveUploadLimit is the single place that turns consent into the number a
// shaper would use. Zero means unthrottled, which only explicit unlimited mode
// can produce: auto without a measurement returns the conservative default, so
// no code path degrades to "no limit".
//
// The metered ceiling is NOT applied here. It depends on a live OS path signal
// (throttle.MeteredSource), not on persisted consent, and folding a live signal
// into a config accessor would make a stored policy look like it says something
// it does not. agent.Snapshot resolves the two together.
func (p ResourcePolicy) EffectiveUploadLimit() (bytesPerSecond uint64, source string) {
	switch NormalizeUploadMode(p.UploadMode) {
	case UploadModeUnlimited:
		return 0, "unlimited (explicit owner choice)"
	case UploadModeManual:
		return p.UploadLimitBytesPerSecond, "manual owner limit"
	default:
		if p.MeasuredUploadBytesPerSecond != 0 {
			return p.MeasuredUploadBytesPerSecond, "measured"
		}
		return throttle.ConservativeBytesPerSecond, "conservative default; not measured yet"
	}
}

// DecodeResourcePolicy accepts the three required integer fields once each, plus
// the optional upload fields. Null, duplicate keys, unknown fields and trailing
// JSON are rejected exactly as before.
//
// Two compatibility decisions, made explicitly rather than by omission:
//   - A body with only the original three fields still decodes, and yields
//     uploadMode "auto" with no manual limit. Older callers (including the
//     shipped Swift UI, which cannot be rebuilt in this sandbox) therefore keep
//     working, and the default they inherit is the shaped one, not unlimited.
//   - measuredUploadBytesPerSecond is accepted and then IGNORED. It appears in
//     the GET response, so a client that reads and writes the policy back must
//     not be rejected for echoing it; but it is derived from measurement, so a
//     client must not be able to assert it either. The caller keeps whatever the
//     agent already measured (agent.SetResourcePolicy).
func DecodeResourcePolicy(body []byte) (ResourcePolicy, error) {
	var p ResourcePolicy
	invalid := errors.New("expected memoryLimitBytes, reserveMemoryBytes and idleSeconds, with optional uploadMode and uploadLimitBytesPerSecond")
	d := json.NewDecoder(bytes.NewReader(body))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return p, invalid
	}
	// Derived fields are parsed and discarded, so a round-tripped GET response is
	// accepted without letting a client set a measured value.
	var discarded uint64
	numbers := map[string]*uint64{"memoryLimitBytes": &p.MemoryLimitBytes,
		"reserveMemoryBytes": &p.ReserveMemoryBytes, "idleSeconds": &p.IdleSeconds,
		"uploadLimitBytesPerSecond":    &p.UploadLimitBytesPerSecond,
		"measuredUploadBytesPerSecond": &discarded,
		// Optional, exactly like the upload fields: absent means the reviewed
		// default, and a three-field body from an older client still decodes.
		"minFreeDiskBytes": &p.MinFreeDiskBytes}
	texts := map[string]*string{"uploadMode": &p.UploadMode}
	required := []string{"memoryLimitBytes", "reserveMemoryBytes", "idleSeconds"}
	seen := make(map[string]bool)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		number, text := numbers[key], texts[key]
		if err != nil || !ok || (number == nil && text == nil) || seen[key] {
			return p, invalid
		}
		seen[key] = true
		if number != nil {
			// A pointer target distinguishes an explicit null from a zero; null
			// must not silently mean "no limit".
			var value *uint64
			if err := d.Decode(&value); err != nil || value == nil {
				return p, invalid
			}
			*number = *value
			continue
		}
		var value *string
		if err := d.Decode(&value); err != nil || value == nil || *value == "" {
			return p, invalid
		}
		*text = *value
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return p, invalid
	}
	for _, key := range required {
		if !seen[key] {
			return p, invalid
		}
	}
	if d.Decode(new(any)) != io.EOF {
		return p, invalid
	}
	p.UploadMode = NormalizeUploadMode(p.UploadMode)
	p.MeasuredUploadBytesPerSecond = 0
	return p, p.Validate()
}
