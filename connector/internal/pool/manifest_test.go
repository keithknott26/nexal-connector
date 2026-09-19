package pool

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTwoDistinctDurableReceiptsVersioningAndRepair(t *testing.T) {
	now := time.Now()
	registry := NewRegistry(func() time.Time { return now })
	a := enrolledIdentity(t, registry)
	b := enrolledIdentity(t, registry)
	c := enrolledIdentity(t, registry)
	source := testStore(t, 1<<20, nil)
	replica := testStore(t, 1<<20, nil)
	data := []byte("project data for two actual stores")
	blob, _ := source.PutBytes(Protected, data)
	if _, err := replica.PutBytes(Protected, data); err != nil {
		t.Fatal(err)
	}
	ar, err := source.Receipt(a, Protected, blob.Digest, now)
	if err != nil {
		t.Fatal(err)
	}
	br, err := replica.Receipt(b, Protected, blob.Digest, now)
	if err != nil {
		t.Fatal(err)
	}
	req := PublishRequest{Key: "project.report", Blob: blob, Owner: "owner", Replicas: []ReplicaReceipt{ar}}
	if _, err := source.Publish(req, registry, now, time.Hour); !errors.Is(err, ErrDurability) {
		t.Fatalf("one replica counted durable: %v", err)
	}
	req.Replicas = []ReplicaReceipt{ar, ar}
	if _, err := source.Publish(req, registry, now, time.Hour); !errors.Is(err, ErrDurability) {
		t.Fatalf("duplicate device counted twice: %v", err)
	}
	req.AllowSingleCopy = true
	single, err := source.Publish(req, registry, now, time.Hour)
	if err != nil || single.Manifest.StateAtCommit != SingleCopyState {
		t.Fatalf("single-copy consent not explicit: %+v %v", single, err)
	}
	req.ExpectedVersion = 1
	req.AllowSingleCopy = false
	req.Replicas = []ReplicaReceipt{ar, br}
	protected, err := source.Publish(req, registry, now, time.Hour)
	if err != nil || protected.Manifest.StateAtCommit != ProtectedState {
		t.Fatalf("two distinct receipts rejected: %+v %v", protected, err)
	}
	if protected.Manifest.PreviousSHA256 != single.SHA256 {
		t.Fatal("manifest history not chained")
	}
	if _, err := source.Publish(req, registry, now, time.Hour); !errors.Is(err, ErrConflict) {
		t.Fatal("stale publisher replaced latest")
	}
	status := ProtectionStatus(protected.Manifest, registry, now, time.Hour, nil)
	if status != ProtectedState {
		t.Fatal(status)
	}
	unavailable := map[string]bool{br.DeviceID: true}
	plan := PlanRepair(protected.Manifest, registry, now, time.Hour, unavailable,
		[]RepairCandidate{{ar.DeviceID, 1 << 20}, {DeviceID(c.PublicKey), 1 << 20}, {DeviceID(c.PublicKey), 1 << 20}})
	if plan.State != DegradedState || len(plan.TargetDevices) != 1 ||
		plan.TargetDevices[0] != DeviceID(c.PublicKey) || plan.SourceDevice != ar.DeviceID {
		t.Fatalf("repair plan %+v", plan)
	}
	unavailable[ar.DeviceID] = true
	plan = PlanRepair(protected.Manifest, registry, now, time.Hour, unavailable, nil)
	if !plan.NeedsBackup || len(plan.TargetDevices) != 0 {
		t.Fatal("repair invented a source")
	}
	if err := registry.Revoke(br.DeviceID); err != nil {
		t.Fatal(err)
	}
	if ProtectionStatus(protected.Manifest, registry, now, time.Hour, nil) != DegradedState {
		t.Fatal("revoked copy still protected")
	}
	if ProtectionStatus(protected.Manifest, registry, now.Add(time.Hour), time.Hour, nil) != UnavailableState {
		t.Fatal("expired receipts still counted")
	}
	old, err := source.ManifestVersion(req.Key, 1)
	if err != nil || old.SHA256 != single.SHA256 {
		t.Fatal("previous version was mutated")
	}
}

func TestReceiptForgeryCacheAndCorruptionNeverConfirmProtected(t *testing.T) {
	now := time.Now()
	registry := NewRegistry(func() time.Time { return now })
	identity := enrolledIdentity(t, registry)
	s := testStore(t, 1<<20, nil)
	data := []byte("abc")
	blob, _ := s.PutBytes(Cache, data)
	receipt, _ := s.Receipt(identity, Cache, blob.Digest, now)
	receipt.Class = Protected
	if err := registry.VerifyReceipt(receipt, now, time.Hour); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("tampered receipt accepted")
	}
	receipt, _ = s.Receipt(identity, Cache, blob.Digest, now)
	blob.Class = Protected
	req := PublishRequest{Key: "test", Blob: blob, Owner: "owner", AllowSingleCopy: true, Replicas: []ReplicaReceipt{receipt}}
	if _, err := s.Publish(req, registry, now, time.Hour); !errors.Is(err, ErrDurability) {
		t.Fatal("evictable cache counted protected")
	}
	if err := os.WriteFile(filepath.Join(s.directory, "cache."+blob.Digest), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Receipt(identity, Cache, blob.Digest, now); !errors.Is(err, ErrIntegrity) {
		t.Fatal("receipt signed corrupted data")
	}
}

func TestBackupRestoreTombstoneAndCatalogIntegrity(t *testing.T) {
	now := time.Now()
	s := testStore(t, 1<<20, nil)
	blob, _ := s.PutBytes(Cache, []byte("independently retained object"))
	req := PublishRequest{Key: "catalog", Blob: blob, Owner: "owner", RetainUntil: now.Add(time.Hour)}
	first, err := s.Publish(req, nil, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tombstone("catalog", 1, now); !errors.Is(err, ErrConflict) {
		t.Fatal("retention bypass")
	}
	second, err := s.Tombstone("catalog", 1, now.Add(time.Hour))
	if err != nil || !second.Manifest.Tombstone || second.Manifest.PreviousSHA256 != first.SHA256 {
		t.Fatalf("bad tombstone: %+v %v", second, err)
	}
	if err := s.Check(Cache, blob.Digest); err != nil {
		t.Fatal("logical deletion erased bytes")
	}
	backup, err := s.Backup(now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	restored := testStore(t, 1<<20, nil)
	if err := restored.RestoreCatalog(backup); err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreCatalog(backup); err != nil {
		t.Fatalf("resume not idempotent: %v", err)
	}
	got, err := restored.Latest("catalog")
	if err != nil || got.SHA256 != second.SHA256 {
		t.Fatalf("restore mismatch: %+v %v", got, err)
	}
	if _, err := restored.Read(Cache, blob.Digest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("catalog restore invented object data")
	}
	data, err := s.Read(Cache, blob.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restored.Put(Cache, blob.Digest, blob.Size, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if err := restored.Check(Cache, blob.Digest); err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), backup...)
	corrupt[len(corrupt)/2] ^= 1
	if err := restored.RestoreCatalog(corrupt); !errors.Is(err, ErrIntegrity) {
		t.Fatal("corrupt backup accepted")
	}
	var forged BackupManifest
	if err := json.Unmarshal(backup, &forged); err != nil {
		t.Fatal(err)
	}
	forged.Records = forged.Records[1:] // No initial history.
	forged.SHA256 = backupHash(forged)
	broken, _ := json.Marshal(forged)
	if err := testStore(t, 1<<20, nil).RestoreCatalog(broken); !errors.Is(err, ErrIntegrity) {
		t.Fatal("broken version chain accepted")
	}
	path := filepath.Join(s.directory, manifestName("catalog", 2))
	if err := os.WriteFile(path, []byte(`{"manifest":{},"sha256":"bad"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Latest("catalog"); !errors.Is(err, ErrIntegrity) {
		t.Fatal("corrupt catalog accepted")
	}
}
