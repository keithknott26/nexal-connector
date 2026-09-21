package p2p

import (
	"errors"
	"sync"
	"testing"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/pool"
)

// TestZeroLimitsAreDefaultsNotUnlimited is the cost control's most important
// property and the easiest one to lose: a blank config field must not uncap spend.
func TestZeroLimitsAreDefaultsNotUnlimited(t *testing.T) {
	n, err := Limits{}.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if n.MaxRelayedConns != DefaultMaxRelayedConns || n.MaxCircuitSeconds != DefaultMaxCircuitSeconds ||
		n.MaxCircuitBytes != DefaultMaxCircuitBytes || n.MaxTotalBytes != DefaultMaxTotalBytes ||
		n.MaxServiceReservations != DefaultMaxServiceReservations {
		t.Fatalf("zero limits did not become the reviewed defaults: %+v", n)
	}
	// No value expresses unlimited, including a huge one.
	for _, bad := range []Limits{
		{MaxTotalBytes: 1 << 60},
		{MaxCircuitBytes: 1 << 60},
		{MaxRelayedConns: 1 << 20},
		{MaxCircuitSeconds: 1 << 40},
		{MaxRelayedConns: -1},
		{MaxServiceReservations: -3},
	} {
		if _, err := bad.Normalize(); !errors.Is(err, errLimits) {
			t.Fatalf("%+v was accepted; a ceiling that can be raised without bound is not a ceiling", bad)
		}
	}
	// A per-circuit ceiling above the total is clamped rather than left to be
	// silently overridden by the total.
	c, err := Limits{MaxCircuitBytes: 1 << 30, MaxTotalBytes: 1 << 20}.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if c.MaxCircuitBytes != 1<<20 {
		t.Fatalf("per-circuit ceiling %d exceeds the total %d", c.MaxCircuitBytes, c.MaxTotalBytes)
	}
}

// TestConnectionCeilingRefusesCircuits covers the first of the plan's three axes.
func TestConnectionCeilingRefusesCircuits(t *testing.T) {
	l := mustLedger(t, Limits{MaxRelayedConns: 2})
	first, err := l.Open("a", nil)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	if _, err := l.Open("b", nil); err != nil {
		t.Fatalf("open 2: %v", err)
	}
	if _, err := l.Open("c", nil); !errors.Is(err, ErrBudget) {
		t.Fatalf("third circuit accepted under a ceiling of 2: %v", err)
	}
	if l.CircuitAvailable() {
		t.Fatal("CircuitAvailable said yes at the ceiling")
	}
	// Closing one frees a slot, so the ceiling is concurrency and not a lifetime
	// quota — those are different controls and conflating them would break restore.
	first.Close()
	if !l.CircuitAvailable() {
		t.Fatal("a closed circuit did not free its slot")
	}
	c := l.Consumption()
	if c.CircuitsRefused == 0 || c.RelayedCircuitsEver != 2 {
		t.Fatalf("refusals not surfaced: %+v", c)
	}
}

// TestTimeCeilingCutsIdleCircuits covers the second axis, and specifically the
// case a byte counter can never catch: a circuit that has STOPPED doing I/O.
func TestTimeCeilingCutsIdleCircuits(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	l, err := NewLedger(Limits{MaxCircuitSeconds: 30}, clock)
	if err != nil {
		t.Fatalf("NewLedger: %v", err)
	}
	c, err := l.Open("a", nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if n := l.EnforceTime(); n != 0 {
		t.Fatalf("cut %d circuits before the deadline", n)
	}
	mu.Lock()
	now = now.Add(31 * time.Second)
	mu.Unlock()
	if n := l.EnforceTime(); n != 1 {
		t.Fatalf("cut %d circuits past the deadline, want 1", n)
	}
	if !c.Expired(clock()) {
		t.Fatal("Expired disagrees with EnforceTime")
	}
	if got := l.Consumption(); got.RelayedCircuitsOpen != 0 || got.CircuitsTruncated != 1 {
		t.Fatalf("time ceiling not surfaced: %+v", got)
	}
}

// TestTotalByteCeilingBoundsSpendAcrossCircuits: a per-circuit cap alone is
// defeated by opening more circuits, so the total is the ceiling that actually
// bounds what a fallback path can cost.
func TestTotalByteCeilingBoundsSpendAcrossCircuits(t *testing.T) {
	l := mustLedger(t, Limits{MaxCircuitBytes: 1 << 10, MaxTotalBytes: 4 << 10, MaxRelayedConns: 8})
	spent := uint64(0)
	for i := 0; i < 32; i++ {
		c, err := l.Open("p", nil)
		if err != nil {
			break
		}
		for {
			if err := l.charge(c, 256); err != nil {
				break
			}
			spent += 256
		}
	}
	if !l.Consumption().Exhausted {
		t.Fatalf("the total ceiling was never reached after %d bytes", spent)
	}
	if got := l.Consumption().RelayBytesUsed; got > (4<<10)+1024 {
		t.Fatalf("spent %d bytes under a 4 KiB total ceiling", got)
	}
	if l.CircuitAvailable() {
		t.Fatal("new circuits were still permitted after the byte budget was spent; that accumulates unusable circuits and looks like a bug instead of a cap")
	}
}

// TestConsumptionStatesItsOwnLimits: a cap an owner cannot see is a constant, not
// a control, and an overstated cap is worse than none. The note must disclose that
// the total resets on restart and is not §16's monthly budget.
func TestConsumptionStatesItsOwnLimits(t *testing.T) {
	c := mustLedger(t, Limits{}).Consumption()
	for _, want := range []string{"per circuit", "total for this process run", "NOT the per-donor monthly budget"} {
		if !contains(c.Note, want) {
			t.Fatalf("Consumption note omits %q: %s", want, c.Note)
		}
	}
	if c.RelayBytesRemaining != DefaultMaxTotalBytes {
		t.Fatalf("remaining=%d", c.RelayBytesRemaining)
	}
}

// TestRelayServiceResourcesCarryTheOwnersCeilings: the relay-SERVICE side is
// libp2p's own accounting, so the only thing that can go wrong is failing to hand
// it the owner's numbers.
func TestRelayServiceResourcesCarryTheOwnersCeilings(t *testing.T) {
	l, err := Limits{MaxRelayedConns: 3, MaxCircuitSeconds: 60, MaxCircuitBytes: 1 << 20, MaxServiceReservations: 5}.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	r := l.RelayResources()
	if r.MaxCircuits != 3 || r.MaxReservations != 5 {
		t.Fatalf("connection ceilings not propagated: %+v", r)
	}
	if r.Limit == nil {
		t.Fatal("relay limits are nil, i.e. the relay service would be UNCAPPED")
	}
	if r.Limit.Duration != 60*time.Second {
		t.Fatalf("duration=%v", r.Limit.Duration)
	}
	// relayv2 applies Data per direction, so the owner's single number is halved
	// to keep "bytes this circuit may move" meaning what it says.
	if r.Limit.Data != (1<<20)/2 {
		t.Fatalf("data=%d", r.Limit.Data)
	}
}

// TestConfigFlagIsOffByDefault: absent config means the whole path stays off, so
// shipping this cannot regress an existing user.
func TestConfigFlagIsOffByDefault(t *testing.T) {
	id, _ := pool.NewIdentity()
	authz := NewCoordinatorAuthorizer([]string{pool.DeviceID(id.PublicKey)})
	if o := OptionsFrom(nil, id, authz, nil); o.Enabled {
		t.Fatal("a config with no p2p block enabled the data plane")
	}
	if o := OptionsFrom(&config.P2P{}, id, authz, nil); o.Enabled {
		t.Fatal("an empty p2p block enabled the data plane")
	}
	o := OptionsFrom(&config.P2P{Enabled: true, MaxTotalBytes: 123 << 20, Listen: []string{"/ip4/127.0.0.1/tcp/0"}}, id, authz, nil)
	if !o.Enabled || o.Limits.MaxTotalBytes != 123<<20 || len(o.Listen) != 1 {
		t.Fatalf("owner configuration not carried: %+v", o)
	}
	// Owner-configurable really means configurable end to end: the value reaches
	// the ledger a live host enforces against.
	h, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = h.Close() }()
	if got := h.Consumption().Limits.MaxTotalBytes; got != 123<<20 {
		t.Fatalf("host enforces %d, owner configured %d", got, 123<<20)
	}
	// And a contradictory block is refused rather than half-honoured.
	if err := (&config.P2P{OfferRelayService: true}).Validate(); err == nil {
		t.Fatal("offerRelayService without enabled was accepted")
	}
	if err := (&config.P2P{Enabled: true, Listen: []string{"127.0.0.1:0"}}).Validate(); err == nil {
		t.Fatal("a host:port was accepted where a multiaddr is required")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
