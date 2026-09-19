package pool

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCombinedConcurrentAdmission(t *testing.T) {
	a, err := NewAdmission(AdmissionOptions{Capacity: Resources{1000, 1000, 1000}, PublicEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var admitted atomic.Int64
	var leasesMu sync.Mutex
	var leases []Reservation
	for idx := 0; idx < 200; idx++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			class := PrivateWork
			if idx%2 == 0 {
				class = PublicWork
			}
			r, err := a.Reserve("attempt", class, Resources{100, 100, 100}, time.Minute)
			if err == nil {
				admitted.Add(1)
				leasesMu.Lock()
				leases = append(leases, r)
				leasesMu.Unlock()
			} else if !errors.Is(err, ErrQuota) {
				t.Errorf("unexpected admission: %v", err)
			}
		}(idx)
	}
	wg.Wait()
	if admitted.Load() != 10 {
		t.Fatalf("admitted %d, expected 10 total across private/public", admitted.Load())
	}
	_, used, _, _ := a.Snapshot()
	if used != (Resources{1000, 1000, 1000}) {
		t.Fatalf("usage %+v", used)
	}
	for _, lease := range leases {
		for idx := 0; idx < 8; idx++ {
			wg.Add(1)
			go func(id string) { defer wg.Done(); a.Release(id) }(lease.ID)
		}
	}
	wg.Wait()
	_, used, _, _ = a.Snapshot()
	if used != (Resources{}) {
		t.Fatalf("non-idempotent releases: %+v", used)
	}
}

func TestAdmissionTTLReclaimPersistentStorageAndOptIn(t *testing.T) {
	now := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	a, _ := NewAdmission(AdmissionOptions{Capacity: Resources{1000, 1000, 1000}, Clock: func() time.Time { return now }})
	if _, err := a.Reserve("cloud", PublicWork, Resources{1, 0, 0}, time.Minute); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("public enabled implicitly: %v", err)
	}
	s := testStore(t, 1000, a)
	b, err := s.PutBytes(Cache, make([]byte, 700))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reserve("private", PrivateWork, Resources{100, 100, 301}, time.Minute); !errors.Is(err, ErrQuota) {
		t.Fatalf("persistent disk double allocated: %v", err)
	}
	r, err := a.Reserve("private", PrivateWork, Resources{1000, 1000, 300}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutBytes(Protected, []byte("x")); !errors.Is(err, ErrQuota) {
		t.Fatalf("storage ignored compute allocation: %v", err)
	}
	now = now.Add(time.Minute)
	expired := a.Expire()
	if len(expired) != 1 || expired[0].ID != r.ID {
		t.Fatalf("expiration %+v", expired)
	}
	if err := a.Validate(r.ID, r.Epoch); !errors.Is(err, ErrExpired) {
		t.Fatal("expired grant valid")
	}
	if _, err := a.Renew(r.ID, r.Epoch, time.Minute); !errors.Is(err, ErrExpired) {
		t.Fatal("expired grant renewed")
	}
	r, _ = a.Reserve("rank", MLXWork, Resources{1000, 1000, 300}, time.Minute)
	stopped := a.Reclaim()
	if len(stopped) != 1 {
		t.Fatal("owner reclaim missed work")
	}
	if err := a.Validate(r.ID, r.Epoch); !errors.Is(err, ErrExpired) {
		t.Fatal("reclaimed epoch valid")
	}
	if _, err := a.Reserve("new", PrivateWork, Resources{1, 0, 0}, time.Minute); !errors.Is(err, ErrPaused) {
		t.Fatal("owner reclaim did not pause")
	}
	_, used, paused, _ := a.Snapshot()
	if !paused || used.StorageBytes != 700 || used.CPUMillis != 0 {
		t.Fatalf("persistent bytes lost during reclaim: %+v", used)
	}
	if err := s.EvictCache(b.Digest); err != nil {
		t.Fatal(err)
	}
	a.Resume()
	if _, err := a.Reserve("new", PrivateWork, Resources{1000, 1000, 1000}, time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionOverflowAndCapacityReduction(t *testing.T) {
	a, _ := NewAdmission(AdmissionOptions{Capacity: Resources{1<<63 - 1, 1<<63 - 1, 1<<63 - 1}})
	r, err := a.Reserve("first", PrivateWork, Resources{1<<63 - 1, 0, 0}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reserve("second", PrivateWork, Resources{1, 0, 0}, time.Minute); !errors.Is(err, ErrQuota) {
		t.Fatalf("overflow admitted: %v", err)
	}
	if err := a.SetCapacity(Resources{1, 1, 1}); !errors.Is(err, ErrQuota) {
		t.Fatalf("shrunk under running work: %v", err)
	}
	if _, err := a.Reserve("invalid", PrivateWork, Resources{-1, 0, 0}, time.Minute); !errors.Is(err, ErrInvalid) {
		t.Fatal("negative resource accepted")
	}
	a.Release(r.ID)
	if err := a.SetCapacity(Resources{1, 1, 1}); err != nil {
		t.Fatal(err)
	}
}
