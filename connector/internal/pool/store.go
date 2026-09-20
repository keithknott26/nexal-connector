package pool

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type StorageClass string

const (
	Cache     StorageClass = "cache"
	Protected StorageClass = "protected"
)

func (c StorageClass) valid() bool { return c == Cache || c == Protected }

type Blob struct {
	Digest string       `json:"digest"`
	Size   int64        `json:"size"`
	Class  StorageClass `json:"class"`
}

type StoreOptions struct {
	Directory      string
	QuotaBytes     int64
	MinFreeBytes   uint64
	MaxObjectBytes int64      // Default 64 MiB; streaming APIs support larger objects.
	Admission      *Admission // Share with all private/public schedulers.
	// FreeBytes overrides the descriptor-based disk probe for tests or a stricter
	// host policy. It must be trustworthy, thread safe and must not call Store.
	FreeBytes func() (uint64, error)
}

// Store is one exclusive process owner per directory, thread-safe within that
// process. The dedicated directory is 0700, every file 0600. The retained root
// descriptor contains operations even if path components are later renamed.
type Store struct {
	mu        sync.Mutex
	root      *os.Root
	dir       *os.File
	lock      *os.File
	directory string
	quota     int64
	minFree   uint64
	maxObject int64
	used      int64
	admission *Admission
	free      func() (uint64, error)
	closed    bool
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validName(s string) bool {
	if len(s) < 1 || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') &&
			!(c >= '0' && c <= '9') && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}

// openDirectory rejects every symlink component, not just the final one.
// The containing directory must be owner-controlled. Existing directories with
// group/world access are rejected rather than silently sharing or chmodding.
func openDirectory(path string) (string, *os.Root, error) {
	if path == "" {
		return "", nil, ErrUnsafePath
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return "", nil, ErrUnsafePath
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil || abs == string(filepath.Separator) {
		return "", nil, ErrUnsafePath
	}
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	current, err := os.OpenRoot("/")
	if err != nil {
		return "", nil, err
	}
	for idx, part := range parts {
		info, err := current.Lstat(part)
		if errors.Is(err, os.ErrNotExist) && idx == len(parts)-1 {
			if err = current.Mkdir(part, 0700); err != nil {
				current.Close()
				return "", nil, err
			}
			info, err = current.Lstat(part)
		}
		if err != nil {
			current.Close()
			return "", nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return "", nil, ErrUnsafePath
		}
		if idx == len(parts)-1 && info.Mode().Perm() != 0700 {
			current.Close()
			return "", nil, ErrUnsafePath
		}
		// Pin each directory descriptor, verifying the inode observed before
		// open. A racing symlink/rename cannot redirect us to another target.
		next, err := current.OpenRoot(part)
		if err != nil {
			current.Close()
			return "", nil, err
		}
		f, err := next.Open(".")
		if err != nil {
			next.Close()
			current.Close()
			return "", nil, err
		}
		opened, err := f.Stat()
		f.Close()
		if err != nil || !os.SameFile(info, opened) {
			next.Close()
			current.Close()
			return "", nil, ErrUnsafePath
		}
		current.Close()
		current = next
	}
	return abs, current, nil
}

func NewStore(o StoreOptions) (*Store, error) {
	if o.QuotaBytes <= 0 || o.MaxObjectBytes < 0 {
		return nil, ErrInvalid
	}
	if o.MaxObjectBytes == 0 {
		o.MaxObjectBytes = 64 << 20
	}
	// Leave one byte for the bounded overrun probe used by Put.
	if o.MaxObjectBytes >= 1<<63-1 {
		return nil, ErrInvalid
	}
	directory, root, err := openDirectory(o.Directory)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, directory: directory, quota: o.QuotaBytes,
		minFree: o.MinFreeBytes, maxObject: o.MaxObjectBytes, admission: o.Admission}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	s.dir, err = root.Open(".")
	if err != nil {
		return nil, err
	}
	info, err := s.dir.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, ErrUnsafePath
	}
	s.lock, err = openNoFollow(root, ".pool-lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("%w: store lock: %v", ErrUnsafePath, err)
	}
	if err := lockFile(s.lock); err != nil {
		return nil, fmt.Errorf("%w: directory already owned", ErrConflict)
	}
	if o.FreeBytes != nil {
		s.free = o.FreeBytes
	} else {
		s.free = func() (uint64, error) { return freeDisk(s.dir) }
	}
	if err := s.scanLocked(); err != nil {
		return nil, err
	}
	if s.admission != nil {
		if err := s.admission.SetStorageUsage(directory, s.used); err != nil {
			return nil, err
		}
	}
	ok = true
	return s, nil
}

func blobName(class StorageClass, digest string) (string, error) {
	if !class.valid() || !validDigest(digest) {
		return "", ErrUnsafePath
	}
	return string(class) + "." + digest, nil
}

func recognizedFile(name string) bool {
	if name == ".pool-lock" {
		return true
	}
	for _, c := range []StorageClass{Cache, Protected} {
		if strings.HasPrefix(name, string(c)+".") && validDigest(strings.TrimPrefix(name, string(c)+".")) {
			return true
		}
	}
	if strings.HasPrefix(name, ".tmp.") {
		return validDigest(strings.TrimPrefix(name, ".tmp."))
	}
	_, _, ok := parseManifestName(name)
	return ok
}

func (s *Store) entriesLocked() ([]os.DirEntry, error) {
	f, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func (s *Store) scanLocked() error {
	entries, err := s.entriesLocked()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !recognizedFile(e.Name()) {
			return fmt.Errorf("%w: directory must contain pool files only", ErrUnsafePath)
		}
		f, err := s.openLocked(e.Name())
		if err != nil {
			return err
		}
		info, err := f.Stat()
		f.Close()
		if err != nil {
			return err
		}
		if strings.HasPrefix(e.Name(), ".tmp.") {
			// A previous exclusive writer crashed before publishing this file.
			if err := s.root.Remove(e.Name()); err != nil {
				return err
			}
			continue
		}
		if info.Size() > s.quota-s.used {
			return ErrQuota
		}
		s.used += info.Size()
	}
	return s.dir.Sync()
}

func (s *Store) openLocked(name string) (*os.File, error) {
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrUnsafePath
	}
	f, err := openNoFollow(s.root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsafePath, err)
	}
	return f, nil
}

func (s *Store) reserveLocked(extra int64) error {
	if extra < 0 || extra > s.quota-s.used {
		return ErrQuota
	}
	free, err := s.free()
	if err != nil {
		return err // Fail closed if the free-disk probe cannot be obtained.
	}
	if free < s.minFree || uint64(extra) > free-s.minFree {
		return ErrHeadroom
	}
	if s.admission != nil {
		if err := s.admission.SetStorageUsage(s.directory, s.used+extra); err != nil {
			return err
		}
	}
	s.used += extra
	return nil
}

func (s *Store) unchargeLocked(bytes int64) {
	s.used -= bytes
	// A closed store has already released its charge; re-registering the key here
	// would resurrect the accounting entry Close deleted.
	if s.admission != nil && !s.closed {
		// Decreasing a registered persistent charge cannot fail.
		_ = s.admission.SetStorageUsage(s.directory, s.used)
	}
}

// writeAtomicLocked publishes a newly named file only, and never mutates an
// existing blob/manifest. fsync(file), no-replace link, fsync(directory) precede success.
// Caller holds exclusive lock and has reserved exactly size bytes.
func (s *Store) writeAtomicLocked(name string, size int64, reader io.Reader, digest string) (committed bool, err error) {
	if _, err := s.root.Lstat(name); err == nil {
		return false, ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	id, err := randomID(32)
	if err != nil {
		return false, err
	}
	tmp := ".tmp." + id
	f, err := openNoFollow(s.root, tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	defer func() {
		f.Close()
		_ = s.root.Remove(tmp)
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(reader, size))
	if err != nil {
		return false, err
	}
	if n != size {
		return false, fmt.Errorf("%w: input size mismatch", ErrIntegrity)
	}
	// Do not write an extra byte or allow an unbounded reader to fill disk.
	var probe [1]byte
	nprobe, probeErr := io.ReadFull(reader, probe[:])
	if nprobe != 0 || probeErr != io.EOF {
		return false, fmt.Errorf("%w: input exceeds declared size", ErrIntegrity)
	}
	if digest != "" && hex.EncodeToString(hash.Sum(nil)) != digest {
		return false, ErrIntegrity
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	// Recheck after allocation: filesystem block/metadata overhead and unrelated
	// applications can consume more space than the logical payload byte count.
	free, err := s.free()
	if err != nil {
		return false, err
	}
	if free < s.minFree {
		return false, ErrHeadroom
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	// Root.Link creates atomically without overwriting an existing destination.
	// It also avoids a race with unexpected same-user filesystem modification.
	if err := s.root.Link(tmp, name); err != nil {
		return false, err
	}
	if err := s.root.Remove(tmp); err != nil {
		return true, err // Conservative accounting: published data occupies disk.
	}
	if err := s.dir.Sync(); err != nil {
		return true, err // Persisted outcome is uncertain; do not issue a receipt.
	}
	return true, nil
}

// Put verifies the caller-specified SHA-256 while streaming exactly size bytes.
// Protected stores only a local copy, not a two-replica durability promise.
// An existing identical object is verified and returned without consuming quota.
func (s *Store) Put(class StorageClass, digest string, size int64, reader io.Reader) (Blob, error) {
	name, err := blobName(class, digest)
	if err != nil || reader == nil || size < 0 || size > s.maxObject {
		return Blob{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Blob{}, ErrClosed
	}
	if f, err := s.verifiedLocked(class, digest); err == nil {
		info, statErr := f.Stat()
		f.Close()
		if statErr != nil {
			return Blob{}, statErr
		}
		if info.Size() != size {
			return Blob{}, ErrIntegrity
		}
		return Blob{digest, size, class}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Blob{}, err
	}
	if err := s.reserveLocked(size); err != nil {
		return Blob{}, err
	}
	committed, err := s.writeAtomicLocked(name, size, reader, digest)
	if !committed {
		s.unchargeLocked(size)
	}
	if err != nil {
		return Blob{}, err
	}
	return Blob{digest, size, class}, nil
}

func (s *Store) PutBytes(class StorageClass, data []byte) (Blob, error) {
	hash := sha256.Sum256(data)
	return s.Put(class, hex.EncodeToString(hash[:]), int64(len(data)), bytes.NewReader(data))
}

func (s *Store) verifiedLocked(class StorageClass, digest string) (*os.File, error) {
	name, err := blobName(class, digest)
	if err != nil {
		return nil, err
	}
	f, err := s.openLocked(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || info.Size() > s.maxObject {
		f.Close()
		return nil, ErrIntegrity
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, s.maxObject+1))
	if err != nil || n != info.Size() || hex.EncodeToString(h.Sum(nil)) != digest {
		f.Close()
		return nil, ErrIntegrity
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// ReadTo validates the complete object before sending any bytes to the writer.
// The owner-controlled files must not be mutated outside this Store.
func (s *Store) ReadTo(class StorageClass, digest string, writer io.Writer) (int64, error) {
	if writer == nil {
		return 0, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	f, err := s.verifiedLocked(class, digest)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return io.Copy(writer, f)
}

func (s *Store) Read(class StorageClass, digest string) ([]byte, error) {
	var out bytes.Buffer
	if _, err := s.ReadTo(class, digest, &out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (s *Store) Check(class StorageClass, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	f, err := s.verifiedLocked(class, digest)
	if err == nil {
		err = f.Close()
	}
	return err
}

// Receipt signs only after an integrity pass and file+directory synchronization.
// The receiving authority must verify enrollment, freshness and distinct IDs.
func (s *Store) Receipt(identity Identity, class StorageClass, digest string, now time.Time) (ReplicaReceipt, error) {
	if !identity.valid() || now.IsZero() {
		return ReplicaReceipt{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ReplicaReceipt{}, ErrClosed
	}
	f, err := s.verifiedLocked(class, digest)
	if err != nil {
		return ReplicaReceipt{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ReplicaReceipt{}, err
	}
	if err := f.Sync(); err != nil {
		return ReplicaReceipt{}, err
	}
	if err := s.dir.Sync(); err != nil {
		return ReplicaReceipt{}, err
	}
	nonce, err := randomID(32)
	if err != nil {
		return ReplicaReceipt{}, err
	}
	v := ReplicaReceipt{DeviceID: DeviceID(identity.PublicKey), Digest: digest,
		Size: info.Size(), Class: class, VerifiedAt: now, Nonce: nonce}
	v.Signature = ed25519.Sign(identity.PrivateKey, receiptMessage(v))
	return v, nil
}

// EvictCache can never delete a protected-class object or a version manifest.
// Protected deletion requires a separately reviewed retention/drain protocol.
func (s *Store) EvictCache(digest string) error {
	name, err := blobName(Cache, digest)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	f, err := s.openLocked(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	info, err := f.Stat()
	f.Close()
	if err != nil {
		return err
	}
	if err := s.root.Remove(name); err != nil {
		return err
	}
	s.unchargeLocked(info.Size())
	return s.dir.Sync()
}

func (s *Store) Usage() (used, quota int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used, s.quota
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	// The bytes stay on disk, but this process no longer owns the directory, so it
	// must stop charging Admission for it: a retained entry can never be deleted
	// again and the host's reported storage total drifts upward with every closed
	// store. A Store reopened on this directory re-registers its scanned total in
	// NewStore, so the charge returns as soon as somebody owns the bytes again.
	if s.admission != nil {
		s.admission.ReleaseStorage(s.directory)
	}
	var result error
	if s.lock != nil {
		result = errors.Join(result, s.lock.Close())
	}
	if s.dir != nil {
		result = errors.Join(result, s.dir.Close())
	}
	if s.root != nil {
		result = errors.Join(result, s.root.Close())
	}
	return result
}

func manifestName(key string, version uint64) string {
	return "manifest." + key + "." + fmt.Sprintf("%020d", version) + ".json"
}

func parseManifestName(name string) (key string, version uint64, ok bool) {
	if !strings.HasPrefix(name, "manifest.") || !strings.HasSuffix(name, ".json") {
		return "", 0, false
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(name, "manifest."), ".json")
	idx := strings.LastIndex(middle, ".")
	if idx < 1 {
		return "", 0, false
	}
	key = middle[:idx]
	ver := middle[idx+1:]
	version, err := strconv.ParseUint(ver, 10, 64)
	return key, version, err == nil && version > 0 && len(ver) == 20 && validName(key)
}
