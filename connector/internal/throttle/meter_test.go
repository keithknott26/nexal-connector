package throttle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// countingMeter funds up to budget bytes and refuses everything after, like the
// internal/budget implementation does once the monthly window is spent.
type countingMeter struct {
	budget  int64
	charged int64
	calls   int
}

func (m *countingMeter) Charge(n int64) error {
	m.calls++
	if m.charged+n > m.budget {
		return fmt.Errorf("charge %d: %w", n, ErrBudgetExhausted)
	}
	m.charged += n
	return nil
}

func TestMeteredConstructorsRejectNilMeter(t *testing.T) {
	limiter, _ := NewLimiter(1000, 100, newFakeClock())
	if _, err := NewMeteredWriter(context.Background(), io.Discard, limiter, nil); err == nil {
		t.Fatal("metered writer accepted a nil meter")
	}
	if _, err := NewMeteredReader(context.Background(), strings.NewReader("x"), limiter, nil); err == nil {
		t.Fatal("metered reader accepted a nil meter")
	}
}

func TestMeteredConstructorsPropagateBaseErrors(t *testing.T) {
	m := &countingMeter{budget: 1}
	if _, err := NewMeteredWriter(context.Background(), io.Discard, nil, m); err == nil {
		t.Fatal("metered writer accepted a nil limiter")
	}
	if _, err := NewMeteredReader(context.Background(), nil, nil, m); err == nil {
		t.Fatal("metered reader accepted a nil reader")
	}
}

// The budget is charged per chunk before the bytes move, so a refusal leaves the
// earlier chunks written and the refused one untouched on the sink.
func TestMeteredWriterStopsAtExhaustedBudget(t *testing.T) {
	// Full bucket (burst 100) so no clock waits are needed for the first 100 bytes.
	limiter, _ := NewLimiter(1000, 100, newFakeClock())
	m := &countingMeter{budget: 100}
	var sink bytes.Buffer
	w, err := NewMeteredWriter(context.Background(), &sink, limiter, m)
	if err != nil {
		t.Fatal(err)
	}
	n, err := w.Write(make([]byte, 250))
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
	if n != 100 || sink.Len() != 100 {
		t.Fatalf("wrote n=%d sink=%d, want 100 and 100", n, sink.Len())
	}
	if m.charged != 100 {
		t.Fatalf("charged %d, want 100", m.charged)
	}
}

// A refusal must be immediate: no shaping delay is paid for refused bytes.
func TestMeteredWriterRefusalDoesNotTouchClock(t *testing.T) {
	clock := newFakeClock()
	limiter, _ := NewLimiter(1000, 100, clock)
	w, _ := NewMeteredWriter(context.Background(), io.Discard, limiter, &countingMeter{budget: 0})
	start := clock.Now()
	if _, err := w.Write(make([]byte, 50)); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
	if !clock.Now().Equal(start) {
		t.Fatal("clock advanced on a refused write")
	}
}

// Reads charge for the bytes asked for (capped at one burst), not the bytes returned.
func TestMeteredReaderChargesRequestedBytes(t *testing.T) {
	limiter, _ := NewLimiter(1000, 100, newFakeClock())
	m := &countingMeter{budget: 100}
	r, err := NewMeteredReader(context.Background(), strings.NewReader("hello"), limiter, m)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 500)
	n, err := r.Read(buf)
	if err != nil || n != 5 {
		t.Fatalf("read n=%d err=%v, want 5 nil", n, err)
	}
	if m.charged != 100 {
		t.Fatalf("charged %d, want 100 (one burst, over-charge on short read)", m.charged)
	}
	if _, err := r.Read(buf); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("err = %v, want ErrBudgetExhausted", err)
	}
}

func TestChargeNilMeterIsFree(t *testing.T) {
	if err := charge(nil, 1<<40); err != nil {
		t.Fatalf("charge(nil) = %v, want nil", err)
	}
}
