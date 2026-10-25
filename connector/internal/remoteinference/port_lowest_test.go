package remoteinference

import "testing"

// A single-Mac server gets the lowest free port every time, so its address survives a restart;
// pools keep moving on through the range.
func TestPickPortLowestReusesFirstFreePort(t *testing.T) {
	r := &Runner{Port: 18601, MaxPort: 18609, used: map[int]bool{}}
	p1, err := r.freePort("127.0.0.1")
	if err != nil || p1 != 18601 {
		t.Fatalf("first pool port %d %v", p1, err)
	}
	r.release(p1)
	if p, _ := r.freePort("127.0.0.1"); p != 18602 {
		t.Fatalf("pool port moved to %d, want 18602", p)
	}
	for i := 0; i < 3; i++ {
		p, err := r.pickPort("127.0.0.1", true)
		if err != nil || p != 18601 {
			t.Fatalf("run %d got %d %v, want 18601", i, p, err)
		}
		r.release(p)
	}
}
