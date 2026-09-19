package pool

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrivateMemoryPoolNeverExportsOrAdmitsPublicCapacity(t *testing.T) {
	a, err := NewPrivateMemoryAdmission(Resources{1000, 1024, 100})
	if err != nil {
		t.Fatal(err)
	}
	if a.PublicAvailable() != (Resources{}) {
		t.Fatal("private RAM advertised")
	}
	if _, err = a.Reserve("cloud", PublicWork, Resources{1, 1, 0}, time.Minute); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("public job admitted to private pool")
	}
	for _, class := range []WorkClass{PrivateWork, MLXWork} {
		r, err := a.Reserve("local", class, Resources{0, 1024, 0}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		a.Release(r.ID)
	}
	if a.PublicAvailable() != (Resources{}) {
		t.Fatal("released private RAM leaked into public pool")
	}
}

func TestMixedPoolPublicJobsCannotBorrowUnusedPrivateMemory(t *testing.T) {
	a, err := NewAdmission(AdmissionOptions{Capacity: Resources{1000, 1000, 1000}, PublicEnabled: true, PrivateMemoryBytes: 700})
	if err != nil {
		t.Fatal(err)
	}
	if a.PublicAvailable().MemoryBytes != 300 {
		t.Fatal("wrong public memory")
	}
	if _, err = a.Reserve("cloud", PublicWork, Resources{0, 301, 0}, time.Minute); !errors.Is(err, ErrQuota) {
		t.Fatal("borrowed private memory")
	}
	pub, err := a.Reserve("cloud", PublicWork, Resources{0, 300, 0}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	local, err := a.Reserve("local", MLXWork, Resources{0, 700, 0}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if a.PublicAvailable().MemoryBytes != 0 {
		t.Fatal("double advertised capacity")
	}
	a.Release(local.ID)
	if a.PublicAvailable().MemoryBytes != 0 {
		t.Fatal("private release expanded public budget")
	}
	a.Release(pub.ID)
	if a.PublicAvailable().MemoryBytes != 300 {
		t.Fatal("wrong released public budget")
	}
}

func TestPrivateReserveCannotBeLostByCapacityShrink(t *testing.T) {
	a, _ := NewAdmission(AdmissionOptions{Capacity: Resources{0, 1000, 0}, PublicEnabled: true, PrivateMemoryBytes: 700})
	if err := a.SetCapacity(Resources{0, 699, 0}); !errors.Is(err, ErrQuota) {
		t.Fatal("lost reserve")
	}
	pub, _ := a.Reserve("cloud", PublicWork, Resources{0, 300, 0}, time.Minute)
	if err := a.SetCapacity(Resources{0, 999, 0}); !errors.Is(err, ErrQuota) {
		t.Fatal("overcommitted public budget")
	}
	a.Release(pub.ID)
	if err := a.SetCapacity(Resources{0, 700, 0}); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateReserveConcurrentPublicAdmission(t *testing.T) {
	a, _ := NewAdmission(AdmissionOptions{Capacity: Resources{0, 1000, 0}, PublicEnabled: true, PrivateMemoryBytes: 700})
	var count atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Reserve("cloud", PublicWork, Resources{0, 100, 0}, time.Minute); err == nil {
				count.Add(1)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 3 {
		t.Fatalf("admitted %d", count.Load())
	}
}

func TestInvalidPrivateReserveRejected(t *testing.T) {
	for _, reserve := range []int64{-1, 1001} {
		if _, err := NewAdmission(AdmissionOptions{Capacity: Resources{0, 1000, 0}, PrivateMemoryBytes: reserve}); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid reserve accepted")
		}
	}
}

func TestPrivateMLXPlansRejectPublicScopeAndCloudFallback(t *testing.T) {
	for _, scope := range []string{"public", "marketplace", "managed", "cloud"} {
		req := placementFixture()
		req.Scope = scope
		if _, err := PlanMLX(req); !errors.Is(err, ErrUnauthorized) {
			t.Fatal("public plan accepted")
		}
	}
	req := placementFixture()
	req.AllowCloudFallback = true
	if _, err := PlanMLX(req); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("cloud fallback accepted")
	}
	req.AllowCloudFallback = false
	plan, err := PlanMLX(req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Scope != "private-lan" || plan.CloudFallbackAllowed || plan.NetworkValidated || plan.ExecutionValidated {
		t.Fatal("incorrect scope or false runtime verification")
	}
}
