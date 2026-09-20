package pool

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

const (
	manifestSchema   = 1
	maxManifestBytes = 1 << 20
	maxBackupBytes   = 16 << 20
)

type ProtectionState string

const (
	CachedState      ProtectionState = "cache"
	ProtectedState   ProtectionState = "protected"
	SingleCopyState  ProtectionState = "single-copy"
	DegradedState    ProtectionState = "degraded"
	UnavailableState ProtectionState = "unavailable"
	DeletedState     ProtectionState = "deleted"
)

type Manifest struct {
	Schema           int       `json:"schema"`
	Key              string    `json:"key"`
	Version          uint64    `json:"version"`
	PreviousSHA256   string    `json:"previousSha256,omitempty"`
	Blob             Blob      `json:"blob"`
	Owner            string    `json:"owner"`
	AllowedReaders   []string  `json:"allowedReaders"`
	EncryptionKeyRef string    `json:"encryptionKeyRef,omitempty"`
	RetainUntil      time.Time `json:"retainUntil,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	// StateAtCommit is historical. Always use ProtectionStatus for a current
	// label: receipts age, machines disappear, membership can be revoked.
	StateAtCommit ProtectionState  `json:"stateAtCommit"`
	Replicas      []ReplicaReceipt `json:"replicas,omitempty"`
	Tombstone     bool             `json:"tombstone"`
}

type ManifestRecord struct {
	Manifest Manifest `json:"manifest"`
	SHA256   string   `json:"sha256"`
}

type PublishRequest struct {
	Key              string
	ExpectedVersion  uint64 // CAS; zero creates the first version.
	Blob             Blob
	Owner            string
	AllowedReaders   []string
	EncryptionKeyRef string // Metadata only: Store does not encrypt plaintext.
	RetainUntil      time.Time
	Replicas         []ReplicaReceipt
	AllowSingleCopy  bool // Explicit consent; never labeled protected.
}

func manifestHash(m Manifest) string {
	b, _ := json.Marshal(m)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func validManifest(m Manifest) bool {
	if m.Schema != manifestSchema || !validName(m.Key) || m.Version == 0 ||
		!validName(m.Owner) || !m.Blob.Class.valid() || !validDigest(m.Blob.Digest) ||
		m.Blob.Size < 0 || len(m.AllowedReaders) > 64 || len(m.Replicas) > 64 ||
		len(m.EncryptionKeyRef) > 512 || m.CreatedAt.IsZero() ||
		(m.Version == 1 && m.PreviousSHA256 != "") ||
		(m.Version > 1 && !validDigest(m.PreviousSHA256)) {
		return false
	}
	for _, reader := range m.AllowedReaders {
		if !validName(reader) {
			return false
		}
	}
	if m.Tombstone {
		return m.StateAtCommit == DeletedState
	}
	if m.Blob.Class == Cache {
		return m.StateAtCommit == CachedState
	}
	return m.StateAtCommit == ProtectedState || m.StateAtCommit == SingleCopyState
}

func strictJSON(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func (s *Store) readRecordLocked(key string, version uint64) (ManifestRecord, error) {
	f, err := s.openLocked(manifestName(key, version))
	if err != nil {
		return ManifestRecord{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return ManifestRecord{}, err
	}
	var rec ManifestRecord
	if len(data) > maxManifestBytes || strictJSON(data, &rec) != nil ||
		!validManifest(rec.Manifest) || rec.Manifest.Key != key || rec.Manifest.Version != version ||
		rec.SHA256 != manifestHash(rec.Manifest) {
		return ManifestRecord{}, ErrIntegrity
	}
	return rec, nil
}

func (s *Store) latestLocked(key string) (ManifestRecord, error) {
	if !validName(key) {
		return ManifestRecord{}, ErrUnsafePath
	}
	entries, err := s.entriesLocked()
	if err != nil {
		return ManifestRecord{}, err
	}
	var latest uint64
	for _, entry := range entries {
		k, version, ok := parseManifestName(entry.Name())
		if ok && k == key && version > latest {
			latest = version
		}
	}
	if latest == 0 {
		return ManifestRecord{}, os.ErrNotExist
	}
	return s.readRecordLocked(key, latest)
}

func (s *Store) Latest(key string) (ManifestRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManifestRecord{}, ErrClosed
	}
	return s.latestLocked(key)
}

func (s *Store) ManifestVersion(key string, version uint64) (ManifestRecord, error) {
	if !validName(key) || version == 0 {
		return ManifestRecord{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManifestRecord{}, ErrClosed
	}
	return s.readRecordLocked(key, version)
}

func (s *Store) appendRecordLocked(rec ManifestRecord) error {
	data, err := json.Marshal(rec)
	if err != nil || len(data) > maxManifestBytes {
		return ErrInvalid
	}
	size := int64(len(data))
	if err := s.reserveLocked(size); err != nil {
		return err
	}
	committed, err := s.writeAtomic(manifestName(rec.Manifest.Key, rec.Manifest.Version), size, bytes.NewReader(data), "")
	if !committed {
		s.unchargeLocked(size)
	}
	return err
}

// Publish creates a versioned catalog record, not a transfer or remote write.
// It rejects protected publication with only one distinct receipt unless the
// caller explicitly consents to a clearly labeled single-copy record.
func (s *Store) Publish(req PublishRequest, registry *Registry, now time.Time, maxReceiptAge time.Duration) (ManifestRecord, error) {
	if req.ExpectedVersion == ^uint64(0) {
		return ManifestRecord{}, ErrInvalid
	}
	m := Manifest{Schema: manifestSchema, Key: req.Key, Version: req.ExpectedVersion + 1,
		Blob: req.Blob, Owner: req.Owner, AllowedReaders: append([]string(nil), req.AllowedReaders...),
		EncryptionKeyRef: req.EncryptionKeyRef, RetainUntil: req.RetainUntil, CreatedAt: now,
		Replicas: append([]ReplicaReceipt(nil), req.Replicas...)}
	for idx := range m.Replicas {
		m.Replicas[idx].Signature = append([]byte(nil), m.Replicas[idx].Signature...)
	}
	if req.Blob.Class == Cache {
		m.StateAtCommit = CachedState
	} else {
		good := confirmedDevices(m, registry, now, maxReceiptAge, nil)
		switch {
		case len(good) >= 2:
			m.StateAtCommit = ProtectedState
		case len(good) == 1 && req.AllowSingleCopy:
			m.StateAtCommit = SingleCopyState
		default:
			return ManifestRecord{}, ErrDurability
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManifestRecord{}, ErrClosed
	}
	prev, err := s.latestLocked(req.Key)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ManifestRecord{}, err
	}
	if prev.Manifest.Version != req.ExpectedVersion {
		return ManifestRecord{}, ErrConflict
	}
	if req.ExpectedVersion > 0 {
		m.PreviousSHA256 = prev.SHA256
	}
	if !validManifest(m) {
		return ManifestRecord{}, ErrInvalid
	}
	rec := ManifestRecord{m, manifestHash(m)}
	if err := s.appendRecordLocked(rec); err != nil {
		return ManifestRecord{}, err
	}
	return rec, nil
}

// Tombstone appends logical deletion while preserving history and blob bytes.
// It is NOT cache eviction and does not claim replicated physical deletion.
func (s *Store) Tombstone(key string, expectedVersion uint64, now time.Time) (ManifestRecord, error) {
	if !validName(key) || expectedVersion == 0 || expectedVersion == ^uint64(0) || now.IsZero() {
		return ManifestRecord{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ManifestRecord{}, ErrClosed
	}
	prev, err := s.latestLocked(key)
	if err != nil {
		return ManifestRecord{}, err
	}
	if prev.Manifest.Version != expectedVersion {
		return ManifestRecord{}, ErrConflict
	}
	if now.Before(prev.Manifest.RetainUntil) {
		return ManifestRecord{}, fmt.Errorf("%w: retention has not expired", ErrConflict)
	}
	m := prev.Manifest
	m.Version++
	m.PreviousSHA256 = prev.SHA256
	m.CreatedAt = now
	m.Tombstone = true
	m.StateAtCommit = DeletedState
	m.Replicas = nil
	rec := ManifestRecord{m, manifestHash(m)}
	if err := s.appendRecordLocked(rec); err != nil {
		return ManifestRecord{}, err
	}
	return rec, nil
}

func confirmedDevices(m Manifest, registry *Registry, now time.Time, maxAge time.Duration, unavailable map[string]bool) []string {
	if registry == nil || maxAge <= 0 || m.Tombstone {
		return nil
	}
	seen := map[string]bool{}
	for _, v := range m.Replicas {
		if unavailable[v.DeviceID] || v.Digest != m.Blob.Digest || v.Size != m.Blob.Size ||
			v.Class != Protected || registry.VerifyReceipt(v, now, maxAge) != nil {
			continue
		}
		seen[v.DeviceID] = true
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ProtectionStatus recomputes current durability from fresh verified signatures.
// unavailable marks observed loss/corruption immediately, even if a receipt is
// still within its freshness window.
func ProtectionStatus(m Manifest, registry *Registry, now time.Time, maxAge time.Duration, unavailable map[string]bool) ProtectionState {
	if m.Tombstone {
		return DeletedState
	}
	if m.Blob.Class == Cache {
		return CachedState
	}
	good := confirmedDevices(m, registry, now, maxAge, unavailable)
	if len(good) >= 2 {
		return ProtectedState
	}
	if len(good) == 0 {
		return UnavailableState
	}
	if m.StateAtCommit == SingleCopyState {
		return SingleCopyState
	}
	return DegradedState
}

type RepairCandidate struct {
	DeviceID       string
	AvailableBytes int64 // Headroom and quota already subtracted by that host.
}

type RepairPlan struct {
	State         ProtectionState `json:"state"`
	Confirmed     []string        `json:"confirmed"`
	SourceDevice  string          `json:"sourceDevice,omitempty"`
	TargetDevices []string        `json:"targetDevices,omitempty"`
	MissingCopies int             `json:"missingCopies"`
	NeedsBackup   bool            `json:"needsBackup"`
}

// PlanRepair never mutates durability or pretends a copy was sent. Execute each
// copy with explicit authorization, verify/sync it, obtain a NEW receipt and
// publish a new version. A healthy source is required; otherwise restore backup.
func PlanRepair(m Manifest, registry *Registry, now time.Time, maxAge time.Duration, unavailable map[string]bool, candidates []RepairCandidate) RepairPlan {
	p := RepairPlan{State: ProtectionStatus(m, registry, now, maxAge, unavailable)}
	if m.Tombstone || m.Blob.Class != Protected {
		return p
	}
	p.Confirmed = confirmedDevices(m, registry, now, maxAge, unavailable)
	p.MissingCopies = 2 - len(p.Confirmed)
	if p.MissingCopies <= 0 {
		p.MissingCopies = 0
		return p
	}
	if len(p.Confirmed) == 0 {
		p.NeedsBackup = true
		return p
	}
	p.SourceDevice = p.Confirmed[0]
	seen := map[string]bool{}
	for _, id := range p.Confirmed {
		seen[id] = true
	}
	ordered := append([]RepairCandidate(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].DeviceID < ordered[j].DeviceID })
	for _, c := range ordered {
		if seen[c.DeviceID] || unavailable[c.DeviceID] || c.AvailableBytes < m.Blob.Size || registry == nil {
			continue
		}
		member, ok := registry.Member(c.DeviceID)
		if !ok || member.RevokedAt != nil || !hasRole(member, Contributor) {
			continue
		}
		seen[c.DeviceID] = true
		p.TargetDevices = append(p.TargetDevices, c.DeviceID)
		if len(p.TargetDevices) == p.MissingCopies {
			break
		}
	}
	return p
}

// BackupManifest contains catalog/history only, not object bytes, membership or
// secret keys. SHA256 detects accidental damage; it is NOT backup authentication.
// Retain it, referenced blobs and separately protected keys off this machine.
type BackupManifest struct {
	Schema    int              `json:"schema"`
	CreatedAt time.Time        `json:"createdAt"`
	Records   []ManifestRecord `json:"records"`
	SHA256    string           `json:"sha256"`
}

func backupHash(b BackupManifest) string {
	b.SHA256 = ""
	data, _ := json.Marshal(b)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Store) Backup(now time.Time) ([]byte, error) {
	if now.IsZero() {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	entries, err := s.entriesLocked()
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	b := BackupManifest{Schema: manifestSchema, CreatedAt: now, Records: []ManifestRecord{}}
	total := 0
	for _, e := range entries {
		key, version, ok := parseManifestName(e.Name())
		if !ok {
			continue
		}
		rec, err := s.readRecordLocked(key, version)
		if err != nil {
			return nil, err
		}
		data, _ := json.Marshal(rec)
		total += len(data)
		if total > maxBackupBytes {
			return nil, ErrQuota
		}
		b.Records = append(b.Records, rec)
	}
	b.SHA256 = backupHash(b)
	data, err := json.Marshal(b)
	if err != nil || len(data) > maxBackupBytes {
		return nil, ErrQuota
	}
	return data, nil
}

// RestoreCatalog imports an independently authenticated backup into an empty
// catalog. A crash can leave a verified prefix; rerunning the same backup safely
// resumes. Historical states are not refreshed: call ProtectionStatus, recheck
// local bytes and obtain new remote receipts before advertising availability.
func (s *Store) RestoreCatalog(data []byte) error {
	if len(data) > maxBackupBytes {
		return ErrQuota
	}
	var b BackupManifest
	if strictJSON(data, &b) != nil || b.Schema != manifestSchema || b.CreatedAt.IsZero() ||
		b.SHA256 != backupHash(b) {
		return ErrIntegrity
	}
	expected := make(map[string]ManifestRecord)
	previous := make(map[string]ManifestRecord)
	sort.Slice(b.Records, func(i, j int) bool {
		a, c := b.Records[i].Manifest, b.Records[j].Manifest
		if a.Key != c.Key {
			return a.Key < c.Key
		}
		return a.Version < c.Version
	})
	for _, rec := range b.Records {
		m := rec.Manifest
		p := previous[m.Key]
		if !validManifest(m) || rec.SHA256 != manifestHash(m) ||
			m.Version != p.Manifest.Version+1 || m.PreviousSHA256 != p.SHA256 {
			return ErrIntegrity
		}
		expected[manifestName(m.Key, m.Version)] = rec
		previous[m.Key] = rec
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	entries, err := s.entriesLocked()
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	for _, e := range entries {
		key, version, ok := parseManifestName(e.Name())
		if !ok {
			continue
		}
		rec, err := s.readRecordLocked(key, version)
		if err != nil || expected[e.Name()].SHA256 != rec.SHA256 {
			return ErrConflict
		}
		existing[e.Name()] = true
	}
	for _, rec := range b.Records {
		if existing[manifestName(rec.Manifest.Key, rec.Manifest.Version)] {
			continue
		}
		if err := s.appendRecordLocked(rec); err != nil {
			return err
		}
	}
	return nil
}
