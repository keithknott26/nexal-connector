package pool

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// writeAtomic must never overwrite an existing name, must fail closed when the
// post-allocation free-space probe fails or has shrunk below the headroom, and
// must leave no temporary file behind on any of those refusals.
func TestWriteAtomicRefusalsLeaveNoTemporaryFiles(t *testing.T) {
	calls := 0
	failAfter := 0 // 0 = never fail
	free := uint64(1 << 30)
	s, err := NewStore(StoreOptions{Directory: testDirectory(t), QuotaBytes: 1 << 20, MinFreeBytes: 10,
		FreeBytes: func() (uint64, error) {
			calls++
			if failAfter > 0 && calls >= failAfter {
				return 0, errors.New("probe failed")
			}
			return free, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	assertNoTemp := func() {
		t.Helper()
		entries, err := os.ReadDir(s.directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tmp.") {
				t.Fatalf("temporary file left behind: %s", e.Name())
			}
		}
	}

	data := []byte("payload")
	committed, err := s.writeAtomic("object-a", int64(len(data)), bytes.NewReader(data), "")
	if err != nil || !committed {
		t.Fatalf("first publish: committed=%v err=%v", committed, err)
	}

	committed, err = s.writeAtomic("object-a", 3, bytes.NewReader([]byte("new")), "")
	if committed || !errors.Is(err, ErrConflict) {
		t.Fatalf("existing name: committed=%v err=%v, want ErrConflict", committed, err)
	}
	if got, _ := os.ReadFile(s.directory + "/object-a"); string(got) != "payload" {
		t.Fatalf("existing object was modified: %q", got)
	}
	assertNoTemp()

	free = 5 // below MinFreeBytes once the data has been allocated
	committed, err = s.writeAtomic("object-b", 3, bytes.NewReader([]byte("abc")), "")
	if committed || !errors.Is(err, ErrHeadroom) {
		t.Fatalf("low headroom: committed=%v err=%v, want ErrHeadroom", committed, err)
	}
	if _, statErr := os.Lstat(s.directory + "/object-b"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused object was published: %v", statErr)
	}
	assertNoTemp()

	free = 1 << 30
	failAfter = calls + 1
	committed, err = s.writeAtomic("object-c", 3, bytes.NewReader([]byte("abc")), "")
	if committed || err == nil {
		t.Fatalf("probe failure: committed=%v err=%v, want failure", committed, err)
	}
	assertNoTemp()
}
