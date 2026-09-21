package p2p

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
)

// DCUtR, AND WHY IT NEEDS A TRACER TO BE A REAL FEATURE.
//
// HARDENING-PLAN lines 880-881: "both peers exchange addresses over the relay,
// measure RTT, then dial simultaneously to punch through". libp2p implements that
// protocol, enabled by libp2p.EnableHolePunching in host.go, and the sequence is
// exactly the three steps the plan names: the initiator opens a /libp2p/dcutr
// stream over the EXISTING relayed connection, sends CONNECT with its observed
// addresses, receives the peer's, uses the CONNECT round trip as the RTT
// measurement, then both sides dial at RTT/2 so each NAT sees an outbound packet
// before the peer's inbound one arrives.
//
// What libp2p does NOT do is tell anybody whether it worked. Without the tracer
// the punch is a silent internal behaviour, and TRANSPORT-NAT-DESIGN.md §5's whole
// complaint about Phase 1 was that an unobservable transport property is one you
// cannot make decisions about. So the tracer is not instrumentation-for-its-own
// sake: it is what makes "is hole punching actually working for this customer"
// answerable, and it is what lets Dialability's honest "plausible but unproven"
// language be upgraded to a measured fact per host.
//
// The RTT recorded here is the DCUtR measurement, not a ping: it is the value the
// synchronised dial was timed against, which is the number that matters when a
// punch fails and the question is whether the timing was the reason.

// PunchStats is the surfaced DCUtR history.
type PunchStats struct {
	Started    uint64 `json:"started"`
	Succeeded  uint64 `json:"succeeded"`
	Failed     uint64 `json:"failed"`
	DirectDial uint64 `json:"directDialSucceeded"`
	// LastRTTMillis is the most recent DCUtR round-trip measurement. Zero means
	// no punch has been attempted, which is not the same as a zero RTT.
	LastRTTMillis int64 `json:"lastRttMillis"`
	// LastError is the most recent punch failure, kept because a punch that fails
	// the same way every time on one customer's network is the diagnosis.
	LastError string `json:"lastError,omitempty"`
	// LANPreferred counts connections this package declined because the peer was
	// reachable on a private address and the existing LAN transport should carry
	// it. Reported next to punch stats because it is the same decision tree.
	LANPreferred uint64 `json:"lanPreferred"`
	// Note keeps the claim honest on the surface itself.
	Note string `json:"note"`
}

// punchTracer implements holepunch.EventTracer. It holds counters only — no
// per-peer map — because an unbounded map keyed by remote peer ID is a memory
// growth path an attacker who can attempt connections controls.
type punchTracer struct {
	started    atomic.Uint64
	succeeded  atomic.Uint64
	failed     atomic.Uint64
	directDial atomic.Uint64
	lanPrefer  atomic.Uint64

	mu      sync.Mutex
	lastRTT time.Duration
	lastErr string
}

func (t *punchTracer) Trace(evt *holepunch.Event) {
	if evt == nil {
		return
	}
	switch e := evt.Evt.(type) {
	case *holepunch.StartHolePunchEvt:
		t.started.Add(1)
		t.mu.Lock()
		t.lastRTT = e.RTT
		t.mu.Unlock()
	case *holepunch.EndHolePunchEvt:
		if e.Success {
			t.succeeded.Add(1)
			return
		}
		t.failed.Add(1)
		t.mu.Lock()
		t.lastErr = e.Error
		t.mu.Unlock()
	case *holepunch.DirectDialEvt:
		if e.Success {
			t.directDial.Add(1)
		}
	}
}

func (t *punchTracer) countLANPreferred() { t.lanPrefer.Add(1) }

func (t *punchTracer) Stats() PunchStats {
	t.mu.Lock()
	rtt, lastErr := t.lastRTT, t.lastErr
	t.mu.Unlock()
	return PunchStats{
		Started:       t.started.Load(),
		Succeeded:     t.succeeded.Load(),
		Failed:        t.failed.Load(),
		DirectDial:    t.directDial.Load(),
		LastRTTMillis: rtt.Milliseconds(),
		LastError:     lastErr,
		LANPreferred:  t.lanPrefer.Load(),
		Note:          "RTT is DCUtR's CONNECT round trip, the value the simultaneous dial was timed against, not a ping; succeeded counts punches that produced a direct connection",
	}
}

var _ holepunch.EventTracer = (*punchTracer)(nil)
