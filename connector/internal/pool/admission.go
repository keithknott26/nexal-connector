package pool

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// Resources uses millicores (1000 = one CPU), and bytes, never installed RAM.
type Resources struct {
	CPUMillis    int64 `json:"cpuMillis"`
	MemoryBytes  int64 `json:"memoryBytes"`
	StorageBytes int64 `json:"storageBytes"`
}

func (r Resources) valid() bool {
	return r.CPUMillis >= 0 && r.MemoryBytes >= 0 && r.StorageBytes >= 0
}

func (r Resources) fits(limit Resources) bool {
	return r.CPUMillis <= limit.CPUMillis && r.MemoryBytes <= limit.MemoryBytes &&
		r.StorageBytes <= limit.StorageBytes
}

func (r Resources) sub(v Resources) Resources {
	return Resources{r.CPUMillis - v.CPUMillis, r.MemoryBytes - v.MemoryBytes, r.StorageBytes - v.StorageBytes}
}

func (r Resources) add(v Resources) Resources {
	return Resources{r.CPUMillis + v.CPUMillis, r.MemoryBytes + v.MemoryBytes, r.StorageBytes + v.StorageBytes}
}

type WorkClass string

const (
	PrivateWork WorkClass = "private"
	PublicWork  WorkClass = "public"
	MLXWork     WorkClass = "mlx"
)

type Reservation struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	Class     WorkClass `json:"class"`
	Resources Resources `json:"resources"`
	Epoch     uint64    `json:"epoch"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type AdmissionOptions struct {
	Capacity           Resources
	PublicEnabled      bool  // Explicit opt-in; does not grant access to private data.
	PrivateMemoryBytes int64 // Non-borrowable by public jobs, even while unused.
	Clock              func() time.Time
}

// Admission is process-local and must be shared by all schedulers on this host.
// Persistent storage charges do not expire or disappear on owner reclaim.
type Admission struct {
	mu            sync.Mutex
	capacity      Resources
	used          Resources
	public        bool
	paused        bool
	epoch         uint64
	now           func() time.Time
	leases        map[string]Reservation
	storage       map[string]int64
	privateMemory int64
}

func NewAdmission(o AdmissionOptions) (*Admission, error) {
	if !o.Capacity.valid() || o.PrivateMemoryBytes < 0 || o.PrivateMemoryBytes > o.Capacity.MemoryBytes {
		return nil, ErrInvalid
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return &Admission{capacity: o.Capacity, public: o.PublicEnabled, privateMemory: o.PrivateMemoryBytes, epoch: 1,
		now: o.Clock, leases: make(map[string]Reservation), storage: make(map[string]int64)}, nil
}

func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func validTTL(ttl time.Duration) bool { return ttl > 0 && ttl <= 24*time.Hour }

func (a *Admission) expireLocked() []Reservation {
	now := a.now()
	var expired []Reservation
	for id, r := range a.leases {
		if !now.Before(r.ExpiresAt) {
			expired = append(expired, r)
			a.used = a.used.sub(r.Resources)
			delete(a.leases, id)
		}
	}
	return expired
}

func (a *Admission) Reserve(owner string, class WorkClass, amount Resources, ttl time.Duration) (Reservation, error) {
	if !validName(owner) || !amount.valid() || amount == (Resources{}) || !validTTL(ttl) ||
		(class != PrivateWork && class != PublicWork && class != MLXWork) {
		return Reservation{}, ErrInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked()
	if a.paused {
		return Reservation{}, ErrPaused
	}
	if class == PublicWork && !a.public {
		return Reservation{}, ErrUnauthorized
	}
	if class == PublicWork && amount.MemoryBytes > a.capacity.MemoryBytes-a.privateMemory-a.publicMemoryLocked() {
		return Reservation{}, ErrQuota
	}
	// Subtract before adding to avoid integer overflow.
	if !amount.fits(a.capacity.sub(a.used)) {
		return Reservation{}, ErrQuota
	}
	id, err := randomID(24)
	if err != nil {
		return Reservation{}, err
	}
	r := Reservation{id, owner, class, amount, a.epoch, a.now().Add(ttl)}
	a.leases[id] = r
	a.used = a.used.add(amount)
	return r, nil
}

// Release is idempotent. A reservation ID is an unguessable capability; never
// expose it to an unrelated public job.
func (a *Admission) Release(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.leases[id]
	if ok {
		a.used = a.used.sub(r.Resources)
		delete(a.leases, id)
	}
	return ok
}

// Validate fences expired/reclaimed attempts. Check before every effect.
func (a *Admission) Validate(id string, epoch uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked()
	r, ok := a.leases[id]
	if !ok || r.Epoch != epoch || epoch != a.epoch {
		return ErrExpired
	}
	return nil
}

func (a *Admission) Renew(id string, epoch uint64, ttl time.Duration) (Reservation, error) {
	if !validTTL(ttl) {
		return Reservation{}, ErrInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked()
	r, ok := a.leases[id]
	if !ok || r.Epoch != epoch || epoch != a.epoch {
		return Reservation{}, ErrExpired
	}
	r.ExpiresAt = a.now().Add(ttl)
	a.leases[id] = r
	return r, nil
}

// Expire returns leases the caller must stop. Admission also lazily expires
// leases on other operations; launchers must independently enforce deadlines.
func (a *Admission) Expire() []Reservation {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.expireLocked()
}

// Reclaim fences every lease, pauses admissions, and returns work to cancel.
// Resume only after cancellation has actually completed. Stored bytes remain.
func (a *Admission) Reclaim() []Reservation {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.paused = true
	a.epoch++
	var stopped []Reservation
	for id, r := range a.leases {
		stopped = append(stopped, r)
		a.used = a.used.sub(r.Resources)
		delete(a.leases, id)
	}
	return stopped
}

func (a *Admission) Resume() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.paused = false
}

// SetStorageUsage reconciles an authoritative persistent storage owner. Stores
// use their directory as key and must have exclusive ownership of that directory.
// It must not be called using untrusted caller-supplied keys or byte counts.
func (a *Admission) SetStorageUsage(owner string, bytes int64) error {
	if owner == "" || len(owner) > 4096 || bytes < 0 {
		return ErrInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked()
	old := a.storage[owner]
	delta := bytes - old
	if delta > 0 {
		if a.paused {
			return ErrPaused
		}
		if delta > a.capacity.StorageBytes-a.used.StorageBytes {
			return ErrQuota
		}
	}
	a.storage[owner] = bytes
	a.used.StorageBytes += delta
	return nil
}

// ReleaseStorage drops an owner's persistent storage charge and the map entry
// itself. A Store calls this from Close: the bytes remain on disk, but this
// process stops accounting for them, and a Store reopened on that directory
// re-registers the scanned total. Without a delete path the entry survives every
// closed store and the reported total only ever grows, which is the number that
// gets billed. Releasing is idempotent; an unknown owner is not an error.
func (a *Admission) ReleaseStorage(owner string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked()
	bytes, ok := a.storage[owner]
	if !ok {
		return
	}
	delete(a.storage, owner)
	a.used.StorageBytes -= bytes
}

func (a *Admission) Snapshot() (capacity, used Resources, paused bool, epoch uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked()
	return a.capacity, a.used, a.paused, a.epoch
}

// SetCapacity cannot silently overcommit existing leases or stored bytes.
func (a *Admission) SetCapacity(capacity Resources) error {
	if !capacity.valid() {
		return ErrInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked()
	if !a.used.fits(capacity) || capacity.MemoryBytes < a.privateMemory ||
		a.publicMemoryLocked() > capacity.MemoryBytes-a.privateMemory {
		return fmt.Errorf("%w: reclaim running work first", ErrQuota)
	}
	a.capacity = capacity
	return nil
}

func (a *Admission) publicMemoryLocked() int64 {
	var memory int64
	for _, lease := range a.leases {
		if lease.Class == PublicWork {
			memory += lease.Resources.MemoryBytes
		}
	}
	return memory
}

// PublicAvailable is the only capacity view intended for marketplace export.
// Never publish Snapshot's total capacity or add another host's RAM to it.
func (a *Admission) PublicAvailable() Resources {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireLocked()
	if !a.public || a.paused {
		return Resources{}
	}
	free := a.capacity.sub(a.used)
	publicMemory := a.capacity.MemoryBytes - a.privateMemory - a.publicMemoryLocked()
	if publicMemory < free.MemoryBytes {
		free.MemoryBytes = publicMemory
	}
	return free
}

// NewPrivateMemoryAdmission never admits public work and exposes zero public
// capacity. It plans local resource reservations, not a remote RAM device.
func NewPrivateMemoryAdmission(capacity Resources) (*Admission, error) {
	return NewAdmission(AdmissionOptions{Capacity: capacity, PrivateMemoryBytes: capacity.MemoryBytes})
}
