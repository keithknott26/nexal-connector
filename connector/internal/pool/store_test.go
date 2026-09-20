package pool

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func testStore(t *testing.T, quota int64, admission *Admission) *Store {
	t.Helper()
	s, err := NewStore(StoreOptions{Directory: testDirectory(t), QuotaBytes: quota,
		MaxObjectBytes: 1 << 20, Admission: admission, FreeBytes: func() (uint64, error) { return 1 << 30, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestImmutableStoreIntegrityAndModes(t *testing.T) {
	s := testStore(t, 1<<20, nil)
	data := []byte("private immutable object")
	b, err := s.PutBytes(Protected, data)
	if err != nil {
		t.Fatal(err)
	}
	if b.Digest != digestOf(data) || b.Class != Protected {
		t.Fatalf("wrong blob: %+v", b)
	}
	got, err := s.Read(Protected, b.Digest)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %q: %v", got, err)
	}
	path := filepath.Join(s.directory, "protected."+b.Digest)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe mode: %v %v", info, err)
	}
	if _, err := s.PutBytes(Protected, data); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != int64(len(data)) {
		t.Fatalf("duplicate charged: %d", used)
	}
	if err := s.EvictCache(b.Digest); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(Protected, b.Digest); err != nil {
		t.Fatalf("cache eviction affected protected: %v", err)
	}
	// Simulate disk damage; a read must emit no corrupt bytes.
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := s.ReadTo(Protected, b.Digest, &output); !errors.Is(err, ErrIntegrity) || output.Len() != 0 {
		t.Fatalf("corruption leaked: %q %v", output.Bytes(), err)
	}
	if _, err := s.PutBytes(Protected, data); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("immutable corrupt blob overwritten: %v", err)
	}
}

func TestQuotaHeadroomAndRollback(t *testing.T) {
	s := testStore(t, 5, nil)
	if _, err := s.PutBytes(Cache, []byte("12345")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutBytes(Protected, []byte("1")); !errors.Is(err, ErrQuota) {
		t.Fatalf("expected quota: %v", err)
	}
	if err := s.EvictCache(digestOf([]byte("12345"))); err != nil {
		t.Fatal(err)
	}
	if used, _ := s.Usage(); used != 0 {
		t.Fatalf("eviction charged %d", used)
	}
	for _, tc := range []struct {
		name string
		body string
		size int64
	}{
		{"short", "12", 3}, {"long", "1234", 3}, {"wrong hash", "xyz", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Put(Cache, digestOf([]byte("123")), tc.size, strings.NewReader(tc.body))
			if !errors.Is(err, ErrIntegrity) {
				t.Fatalf("expected integrity: %v", err)
			}
			if used, _ := s.Usage(); used != 0 {
				t.Fatalf("failed write left quota: %d", used)
			}
			files, _ := os.ReadDir(s.directory)
			if len(files) != 1 || files[0].Name() != ".pool-lock" {
				t.Fatalf("failed write exposed files: %v", files)
			}
		})
	}
	s2, err := NewStore(StoreOptions{Directory: testDirectory(t), QuotaBytes: 100,
		MinFreeBytes: 10, FreeBytes: func() (uint64, error) { return 12, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.PutBytes(Cache, []byte("abc")); !errors.Is(err, ErrHeadroom) {
		t.Fatalf("expected headroom rejection: %v", err)
	}
	s3, err := NewStore(StoreOptions{Directory: testDirectory(t), QuotaBytes: 100,
		FreeBytes: func() (uint64, error) { return 0, errors.New("probe failed") }})
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	if _, err := s3.PutBytes(Cache, []byte("a")); err == nil {
		t.Fatal("failed disk probe admitted bytes")
	}
	probes := 0
	s4, err := NewStore(StoreOptions{Directory: testDirectory(t), QuotaBytes: 100, MinFreeBytes: 10,
		FreeBytes: func() (uint64, error) {
			probes++
			if probes == 1 {
				return 100, nil
			}
			return 9, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer s4.Close()
	if _, err := s4.PutBytes(Cache, []byte("a")); !errors.Is(err, ErrHeadroom) {
		t.Fatalf("post-allocation headroom not checked: %v", err)
	}
	if used, _ := s4.Usage(); used != 0 {
		t.Fatal("post-allocation failure did not roll back")
	}
}

func TestStoreConcurrencyAndReopen(t *testing.T) {
	s := testStore(t, 10, nil)
	var wg sync.WaitGroup
	for idx := 0; idx < 40; idx++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PutBytes(Cache, []byte("1234567890")); err != nil {
				t.Errorf("concurrent duplicate: %v", err)
			}
		}()
	}
	wg.Wait()
	if used, _ := s.Usage(); used != 10 {
		t.Fatalf("usage %d", used)
	}
	if _, err := NewStore(StoreOptions{Directory: s.directory, QuotaBytes: 100}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second writer accepted: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(StoreOptions{Directory: s.directory, QuotaBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if used, _ := reopened.Usage(); used != 10 {
		t.Fatalf("restart lost usage: %d", used)
	}
	if _, err := reopened.PutBytes(Cache, []byte("x")); !errors.Is(err, ErrQuota) {
		t.Fatalf("restart oversubscribed: %v", err)
	}
}

func TestTraversalSymlinkAndHardlinkRejection(t *testing.T) {
	s := testStore(t, 1<<20, nil)
	for _, digest := range []string{"../secret", "/etc/passwd", strings.Repeat("A", 64), "", "a/../b"} {
		if _, err := s.Read(Cache, digest); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("accepted %q: %v", digest, err)
		}
	}
	if _, err := s.Latest("../escape"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("manifest traversal: %v", err)
	}
	outside := testDirectory(t)
	secret := []byte("must not expose external files")
	external := filepath.Join(outside, "secret")
	if err := os.WriteFile(external, secret, 0600); err != nil {
		t.Fatal(err)
	}
	hash := digestOf(secret)
	target := filepath.Join(s.directory, "cache."+hash)
	if err := os.Symlink(external, target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(Cache, hash); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink accepted: %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(external, target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(Cache, hash); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("external hardlink accepted: %v", err)
	}
	ancestor := filepath.Join(testDirectory(t), "redirect")
	if err := os.Symlink(outside, ancestor); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{ancestor, filepath.Join(ancestor, "new"), outside + "/../new"} {
		if _, err := NewStore(StoreOptions{Directory: path, QuotaBytes: 100}); !errors.Is(err, ErrUnsafePath) {
			t.Errorf("unsafe root accepted %q: %v", path, err)
		}
	}
}

func TestDirectoryDescriptorPinsOriginalRoot(t *testing.T) {
	parent := testDirectory(t)
	path := filepath.Join(parent, "store")
	s, err := NewStore(StoreOptions{Directory: path, QuotaBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	outside := testDirectory(t)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	b, err := s.PutBytes(Cache, []byte("safe"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "cache."+b.Digest)); err != nil {
		t.Fatalf("did not use pinned directory: %v", err)
	}
	files, _ := os.ReadDir(outside)
	if len(files) != 0 {
		t.Fatalf("escaped into symlink directory: %v", files)
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestReaderErrorAndClosedStore(t *testing.T) {
	s := testStore(t, 100, nil)
	if _, err := s.Put(Cache, digestOf([]byte("x")), 1, failedReader{}); err == nil {
		t.Fatal("failed reader accepted")
	}
	if used, _ := s.Usage(); used != 0 {
		t.Fatal("failed reader left usage")
	}
	s.Close()
	s.Close()
	if _, err := s.PutBytes(Cache, []byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

// Close must hand the directory's persistent charge back to Admission. Before
// this, every open/close cycle left a storage entry nobody could ever delete,
// so the host's reported (billed) storage total only grew.
func TestStorageAccountingReleasedOnCloseAndReclaimedOnReopen(t *testing.T) {
	a, err := NewAdmission(AdmissionOptions{Capacity: Resources{1000, 1000, 1000}})
	if err != nil {
		t.Fatal(err)
	}
	directory := testDirectory(t)
	open := func() *Store {
		s, err := NewStore(StoreOptions{Directory: directory, QuotaBytes: 1000,
			MaxObjectBytes: 1 << 20, Admission: a, FreeBytes: func() (uint64, error) { return 1 << 30, nil }})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	stored := func() int64 {
		_, used, _, _ := a.Snapshot()
		return used.StorageBytes
	}
	s := open()
	if _, err := s.PutBytes(Cache, make([]byte, 600)); err != nil {
		t.Fatal(err)
	}
	if stored() != 600 {
		t.Fatalf("stored bytes not charged: %d", stored())
	}
	// Three cycles: a leaking charge shows up as 600, 1200, 1800.
	for cycle := 0; cycle < 3; cycle++ {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if stored() != 0 {
			t.Fatalf("cycle %d retained a charge for a closed store: %d", cycle, stored())
		}
		s = open()
		if stored() != 600 {
			t.Fatalf("cycle %d did not recharge the reopened store: %d", cycle, stored())
		}
	}
	// A repeated Close cannot double-release into a negative total, and a
	// still-open store elsewhere keeps its own charge.
	other := testStore(t, 1000, a)
	if _, err := other.PutBytes(Cache, make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if stored() != 100 {
		t.Fatalf("release disturbed another owner or underflowed: %d", stored())
	}
	// Capacity freed by the release is grantable again, which is the point.
	lease, err := a.Reserve("private", PrivateWork, Resources{0, 0, 900}, time.Minute)
	if err != nil {
		t.Fatalf("released storage not reusable: %v", err)
	}
	a.Release(lease.ID)
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if stored() != 0 {
		t.Fatalf("charge survived the last owner: %d", stored())
	}
}

// gatedReader parks after its first byte until the test releases it, standing in
// for a peer that streams an object slowly over the network.
type gatedReader struct {
	data    []byte
	off     int
	gate    chan struct{}
	started chan struct{}
	once    sync.Once
}

func (r *gatedReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	if r.off > 0 {
		<-r.gate
	}
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := 1
	if r.off > 0 {
		n = len(r.data) - r.off
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[r.off:r.off+n])
	r.off += n
	return n, nil
}

func newGatedReader(data []byte) *gatedReader {
	return &gatedReader{data: data, gate: make(chan struct{}), started: make(chan struct{})}
}

// A Put must not hold the store's only mutex across the byte stream: every other
// caller on the host used to block for as long as the slowest uploader took.
func TestPutStreamsWithoutHoldingStoreMutex(t *testing.T) {
	s := testStore(t, 1<<20, nil)
	existing, err := s.PutBytes(Cache, []byte("readable during a slow upload"))
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("s"), 4096)
	reader := newGatedReader(payload)
	done := make(chan error, 1)
	go func() {
		_, err := s.Put(Protected, digestOf(payload), int64(len(payload)), reader)
		done <- err
	}()
	<-reader.started
	operations := make(chan error, 1)
	go func() {
		if _, err := s.Read(Cache, existing.Digest); err != nil {
			operations <- err
			return
		}
		if used, _ := s.Usage(); used < int64(len(payload)) {
			operations <- errors.New("in-flight reservation not visible")
			return
		}
		operations <- s.Check(Cache, existing.Digest)
	}()
	select {
	case err := <-operations:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("readers blocked on an in-flight Put: the mutex is still held across the stream")
	}
	close(reader.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read(Protected, digestOf(payload)); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("streamed object not published: %v", err)
	}
}

// Two callers uploading the same object concurrently must charge quota once. The
// second waits for the first instead of reserving a second copy's worth of bytes.
func TestConcurrentDuplicatePutChargesQuotaOnce(t *testing.T) {
	payload := bytes.Repeat([]byte("q"), 4096)
	s := testStore(t, int64(len(payload)), nil) // Exactly one copy fits.
	reader := newGatedReader(payload)
	first := make(chan error, 1)
	go func() {
		_, err := s.Put(Protected, digestOf(payload), int64(len(payload)), reader)
		first <- err
	}()
	<-reader.started
	second := make(chan error, 1)
	go func() {
		_, err := s.Put(Protected, digestOf(payload), int64(len(payload)), bytes.NewReader(payload))
		second <- err
	}()
	time.Sleep(20 * time.Millisecond) // Let the duplicate reach its wait.
	close(reader.gate)
	if err := <-first; err != nil {
		t.Fatalf("streaming writer: %v", err)
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("duplicate upload rejected instead of deduplicated: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("duplicate upload never woke up")
	}
	if used, _ := s.Usage(); used != int64(len(payload)) {
		t.Fatalf("one object charged twice: %d", used)
	}
}

// Close must not wait on network I/O, must release accounting at once, and the
// last writer out must release the descriptors and the directory flock.
func TestCloseDoesNotBlockOnStreamingPut(t *testing.T) {
	a, err := NewAdmission(AdmissionOptions{Capacity: Resources{1000, 1000, 1 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	directory := testDirectory(t)
	options := StoreOptions{Directory: directory, QuotaBytes: 1 << 20, MaxObjectBytes: 1 << 20,
		Admission: a, FreeBytes: func() (uint64, error) { return 1 << 30, nil }}
	s, err := NewStore(options)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := s.PutBytes(Cache, []byte("survives the shutdown"))
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("c"), 4096)
	reader := newGatedReader(payload)
	done := make(chan error, 1)
	go func() {
		_, err := s.Put(Protected, digestOf(payload), int64(len(payload)), reader)
		done <- err
	}()
	<-reader.started
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Close blocked on a streaming write")
	}
	if _, used, _, _ := a.Snapshot(); used.StorageBytes != 0 {
		t.Fatalf("charge retained while shutting down: %d", used.StorageBytes)
	}
	close(reader.gate)
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("object published into a closed store: %v", err)
	}
	reopened, err := NewStore(options)
	if err != nil {
		t.Fatalf("last writer did not release the directory: %v", err)
	}
	defer reopened.Close()
	if err := reopened.Check(Cache, kept.Digest); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Check(Protected, digestOf(payload)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted write left a published object: %v", err)
	}
}
