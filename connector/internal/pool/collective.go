package pool

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"time"
)

// The ring collective: a reduce-scatter followed by an all-gather, over the
// mutual-TLS, fingerprint-pinned peer transport already in this package.
//
// Why this exists at all, and what it deliberately is not:
//
//   - HARDENING-PLAN §39.1 records MLX's backend matrix: `ring` is the TCP
//     backend and is "always available"; `jaccl` is RDMA over Thunderbolt.
//     §39.2 states plainly that JACCL cannot run over TCP/IP, and that "any
//     collective we build on TCP must be expressible as all-reduce / all-gather
//     around a ring". That sentence is the specification this file implements.
//   - §39.4 option 1, kept and strengthened by §40, is that MLX stays out of the
//     connector: "we implement the ring collective ourselves in Go over the
//     existing authenticated peer transport". So this is not MLX's ring, it does
//     not link MLX, and it does not shell out to `mlx.launch` (which SSHes into
//     every node — a different trust and transport model entirely). It adds no
//     dependency: go.mod still has zero require lines.
//   - §49.4 is the security consequence that shapes the membership rules below:
//     a peer reachable over a Thunderbolt cable "is still just an IP peer", and
//     "physical proximity is not authorization". AllowedPeers — and therefore
//     ring membership — still derives from the coordinator's list alone.
//
// What is NOT implemented here, on purpose: §49's LinkClass, AllowedBackends and
// MinLinkClass ladder. §49.6 records those as specification only and they do not
// exist in placement.go; a rung selector that cannot observe a link class would
// be a gate that cannot be honoured, and §50 is a UI specification whose inputs
// do not exist either. Neither is invented here.
//
// What cannot be verified from this environment is stated in the tests and in the
// task report rather than implied away: there is no second Mac, no Apple silicon,
// no Thunderbolt hardware, no MLX and no physical network here. §42.6 requires
// this collective to be differentially tested against MLX's own `ring` backend
// before any claim of numerical agreement with MLX is made. That test does not
// exist and cannot exist here.

// ReduceOp is the reduction applied element-wise. It is carried in every frame
// and checked against the local operation, because two ranks reducing with
// different operations would silently produce a result that is neither.
type ReduceOp uint8

const (
	ReduceSum ReduceOp = iota + 1
	ReduceMax
	ReduceMin
)

func (o ReduceOp) valid() bool {
	return o == ReduceSum || o == ReduceMax || o == ReduceMin
}

func (o ReduceOp) apply(a, b float64) float64 {
	switch o {
	case ReduceMax:
		if b > a {
			return b
		}
		return a
	case ReduceMin:
		if b < a {
			return b
		}
		return a
	default:
		return a + b
	}
}

// Abort reason strings. They are internal to this module — both ends of a ring
// run this same Go code from this same module — so they are not a cross-repository
// contract and must not be turned into one.
const (
	reasonUnreachable = "rank-unreachable"
	reasonProtocol    = "protocol-violation"
	reasonCancelled   = "local-context-cancelled"
)

// RingMember is one participant. Identity is the 64-hex-character key
// fingerprint (DeviceID) and nothing else: not a name, not an address. The
// endpoint is a dial hint that the TLS handshake then has to agree with, exactly
// as in PeerClient — whoever answers the address must present the pinned key, or
// the connection is refused.
type RingMember struct {
	// Fingerprint is DeviceID: 64 lowercase hex characters of SHA-256 over the
	// device's ed25519 public key. It is the only field that identifies a peer.
	Fingerprint string
	// Tenant is the tenant scope this member is authorized in. It must equal the
	// ring's tenant. A member carrying a different scope is refused rather than
	// silently rescoped: §3's isolation is not something a topology may relax.
	Tenant string
	// Endpoint is an https:// URL with a literal private or loopback IP and an
	// explicit port, the same shape NewPeerClient accepts. It must be empty for
	// this host's own entry, which is served rather than dialed.
	Endpoint string
}

// RingOptions configures one rank's view of the ring.
type RingOptions struct {
	Identity Identity
	Registry *Registry
	// Tenant is mandatory. There is no default and no empty scope: a collective
	// that cannot say which tenant it belongs to is an error, not a global one.
	//
	// It is supplied by the caller because the coordinator never sends a tenant
	// identifier to a connector — internal/client/peers.go records that the
	// tenant "is never sent: it is read server-side from the host token's row".
	// So this value is a locally configured scope label, and the only honest
	// thing this package can do with it is require that every member agrees on
	// it and that every frame carries it. It is not proof of tenancy and is not
	// attested; the coordinator's authorization list is what carries weight.
	Tenant string
	// AuthorizedPeers is the coordinator-sourced allowlist, i.e. exactly what
	// discovery.AllowedPeers returns. DISCOVERY NEVER AUTHORIZES: an mDNS
	// candidate the coordinator did not list is absent from that slice, and
	// NewRing refuses any member that is not in it. That refusal is the only
	// reason a hostile LAN — or a Thunderbolt cable — cannot add a rank.
	AuthorizedPeers []string
	// Members must include this host. Rank order is derived from the set rather
	// than from this slice's order.
	Members []RingMember
	// StepTimeout bounds one send and one receive. Zero selects
	// defaultRingStepTimeout. A step that exceeds it is a failed rank, not a
	// reason to wait forever.
	StepTimeout time.Duration
	Clock       func() time.Time
}

const (
	// defaultRingStepTimeout is the per-step ceiling. A LAN chunk transfer is
	// milliseconds; 30 s is slack for a machine under memory pressure, not a
	// budget to be spent.
	defaultRingStepTimeout = 30 * time.Second
	maxRingStepTimeout     = 10 * time.Minute
)

// RingFailure names the rank a survivor believes failed, so a collective that
// cannot complete produces a report rather than a hang. It wraps
// ErrRankUnreachable when this host observed the failure itself, and ErrAborted
// when another rank told us.
type RingFailure struct {
	Rank        int
	Fingerprint string
	Reason      string
	err         error
}

func (f *RingFailure) Error() string {
	return "pool: ring rank " + strconv.Itoa(f.Rank) + " (" + f.Fingerprint + "): " + f.Reason
}

func (f *RingFailure) Unwrap() error { return f.err }

// Ring is one rank's membership in a ring plus both halves of its transport: the
// authenticated inbox its predecessor posts chunks to, and the pinned clients it
// posts chunks to its successor with.
//
// Nothing binds or dials at construction. Serve takes an already-bound private
// listener, exactly like PeerServer, so the caller keeps lifecycle control.
type Ring struct {
	tenant      string
	self        string
	members     []RingMember // Index is rank.
	rank        int
	stepTimeout time.Duration
	now         func() time.Time

	transport *ringTransport
}

// resolveRingMembers applies every membership rule that does not depend on rank
// order: fingerprint shape, no duplicates, mandatory matching tenant scope,
// coordinator authorization for every peer, a usable endpoint for every peer, and
// this host's own presence.
func resolveRingMembers(o RingOptions, self string) (map[string]RingMember, error) {
	authorized := map[string]bool{}
	for _, fingerprint := range o.AuthorizedPeers {
		if !validDigest(fingerprint) {
			// The coordinator is trusted for authorization, not for well-formed
			// output; a fingerprint we cannot pin is one we cannot dial.
			return nil, fmt.Errorf("%w: authorized peer is not a key fingerprint", ErrInvalid)
		}
		authorized[fingerprint] = true
	}
	members := make(map[string]RingMember, len(o.Members))
	haveSelf := false
	for _, m := range o.Members {
		if !validDigest(m.Fingerprint) {
			return nil, fmt.Errorf("%w: ring member is not a key fingerprint", ErrInvalid)
		}
		if _, duplicate := members[m.Fingerprint]; duplicate {
			return nil, fmt.Errorf("%w: duplicate ring member", ErrInvalid)
		}
		if m.Tenant != o.Tenant {
			// Cross-tenant membership is refused, not reconciled.
			return nil, fmt.Errorf("%w: member %s is scoped to another tenant", ErrUnauthorized, m.Fingerprint)
		}
		if m.Fingerprint == self {
			if m.Endpoint != "" {
				return nil, fmt.Errorf("%w: this host's own ring entry must carry no endpoint", ErrInvalid)
			}
			haveSelf = true
			members[self] = RingMember{Fingerprint: self, Tenant: o.Tenant}
			continue
		}
		if !authorized[m.Fingerprint] {
			// The invariant this whole file is shaped by. A peer that only
			// appeared on mDNS is absent from the coordinator's list, so it lands
			// here and is refused. Presence on a link — including a directly
			// cabled Thunderbolt link (§49.4) — grants no standing at all.
			return nil, fmt.Errorf("%w: %s is not in the coordinator's authorized list", ErrUnauthorized, m.Fingerprint)
		}
		if _, err := validRingEndpoint(m.Endpoint); err != nil {
			return nil, err
		}
		members[m.Fingerprint] = m
	}
	if !haveSelf {
		return nil, fmt.Errorf("%w: this host is not a member of the ring", ErrInvalid)
	}
	return members, nil
}

// newRing is the single construction path. order is the rank assignment, already
// resolved by the caller, so there is exactly one place that builds a transport
// and exactly one place that decides who rank 0 is.
func newRing(o RingOptions, order []RingMember, self string) (*Ring, error) {
	if len(order) < 2 || len(order) > MaxRingRanks {
		// One rank is not a ring, and a ring cannot be grown past its bound.
		return nil, fmt.Errorf("%w: a ring needs 2 to %d ranks", ErrInvalid, MaxRingRanks)
	}
	rank := slices.IndexFunc(order, func(m RingMember) bool { return m.Fingerprint == self })
	if rank < 0 {
		return nil, ErrInvalid
	}
	step := o.StepTimeout
	if step == 0 {
		step = defaultRingStepTimeout
	}
	r := &Ring{tenant: o.Tenant, self: self, members: order, rank: rank, stepTimeout: step}
	transport, err := newRingTransport(o, r)
	if err != nil {
		return nil, err
	}
	r.transport = transport
	r.now = transport.policy.now
	return r, nil
}

// NewRing builds the deterministic ring for this host.
//
// Rank ordering is the ascending lexicographic order of the member fingerprints.
// That choice is deliberate: it depends on no address, no hostname, no discovery
// order, no clock and no startup order, so every rank independently computes the
// identical ring from the identical authorized set without a coordination round
// trip. Two ranks that disagree about membership therefore disagree visibly —
// their frames fail this file's per-step checks — rather than forming two
// half-rings that each wait for the other.
func NewRing(o RingOptions) (*Ring, error) {
	if !validCollectiveText(o.Tenant, maxCollectiveTenant) {
		return nil, fmt.Errorf("%w: tenant scope is mandatory for a ring collective", ErrInvalid)
	}
	if o.StepTimeout < 0 || o.StepTimeout > maxRingStepTimeout {
		return nil, ErrInvalid
	}
	self := DeviceID(o.Identity.PublicKey)
	if !validDigest(self) {
		return nil, ErrInvalid
	}
	members, err := resolveRingMembers(o, self)
	if err != nil {
		return nil, err
	}
	order := make([]RingMember, 0, len(members))
	for _, m := range members {
		order = append(order, m)
	}
	slices.SortFunc(order, func(a, b RingMember) int {
		switch {
		case a.Fingerprint < b.Fingerprint:
			return -1
		case a.Fingerprint > b.Fingerprint:
			return 1
		}
		return 0
	})
	return newRing(o, order, self)
}

// NewRingFromPlan builds the ring for a placement plan produced by PlanMLX,
// preserving the plan's rank assignment rather than re-deriving one: the plan
// already placed rank i on a specific device with that rank's memory estimate, so
// a ring that reordered them would run each rank's work on the wrong machine.
// The plan's order is itself deterministic (PlanMLX documents its placement as
// deterministic, not an optimizer), so this keeps the property NewRing provides.
//
// A JACCL plan is refused. JACCL is RDMA over Thunderbolt (§39.1) and does not
// run over TCP/IP at all (§39.2), while this transport is TCP. Quietly running a
// JACCL plan over a TCP ring would be exactly the silent downgrade §49.3 refuses,
// and the unconditional JACCL launch refusal in the bridge runtime stays shut
// (§39.3). A caller that wants the TCP ring must ask for the TCP ring.
func NewRingFromPlan(o RingOptions, plan PlacementPlan) (*Ring, error) {
	if plan.Backend != TCPRing {
		return nil, fmt.Errorf("%w: a %q plan cannot run over the TCP ring transport", ErrUnsupported, plan.Backend)
	}
	if !validCollectiveText(o.Tenant, maxCollectiveTenant) {
		return nil, fmt.Errorf("%w: tenant scope is mandatory for a ring collective", ErrInvalid)
	}
	if o.StepTimeout < 0 || o.StepTimeout > maxRingStepTimeout {
		return nil, ErrInvalid
	}
	self := DeviceID(o.Identity.PublicKey)
	if !validDigest(self) {
		return nil, ErrInvalid
	}
	members, err := resolveRingMembers(o, self)
	if err != nil {
		return nil, err
	}
	if len(plan.Ranks) != len(members) {
		return nil, fmt.Errorf("%w: the plan places %d ranks on %d ring members", ErrInvalid, len(plan.Ranks), len(members))
	}
	order := make([]RingMember, 0, len(plan.Ranks))
	for index, placement := range plan.Ranks {
		if placement.Rank != index {
			return nil, fmt.Errorf("%w: plan ranks are not contiguous", ErrInvalid)
		}
		member, ok := members[placement.DeviceID]
		if !ok {
			// A placed device that is not an authorized ring member is a plan for
			// a machine this host may not dial. Fail closed.
			return nil, fmt.Errorf("%w: placed device %s is not an authorized ring member", ErrUnauthorized, placement.DeviceID)
		}
		order = append(order, member)
	}
	return newRing(o, order, self)
}

// Rank is this host's position in the ring, and Size the number of ranks.
func (r *Ring) Rank() int { return r.rank }
func (r *Ring) Size() int { return len(r.members) }

// Tenant is the mandatory scope every frame carries.
func (r *Ring) Tenant() string { return r.tenant }

// Members returns the ring in rank order. The slice is a copy, so a caller
// cannot change who rank 0 is by mutating it.
func (r *Ring) Members() []RingMember { return slices.Clone(r.members) }

// Successor and Predecessor are the only two peers a rank exchanges data with.
// §39.2: `ring` is a ring, not a mesh — rank i talks only to i±1, and arbitrary
// point-to-point send/recv is not part of this backend.
func (r *Ring) Successor() RingMember   { return r.members[(r.rank+1)%len(r.members)] }
func (r *Ring) Predecessor() RingMember { return r.members[(r.rank-1+len(r.members))%len(r.members)] }

// Serve accepts collective frames on an already-bound private listener. It
// validates the bound address before taking ownership, the same rule as
// PeerServer.Serve.
func (r *Ring) Serve(listener net.Listener) error { return r.transport.serve(listener) }

// Shutdown stops serving and releases idle peer connections.
func (r *Ring) Shutdown(ctx context.Context) error { return r.transport.shutdown(ctx) }

// Close stops serving immediately. A Ring that has been closed refuses to run a
// collective rather than starting one it cannot finish.
func (r *Ring) Close() error { return r.transport.Close() }

// chunkBounds splits total values into len(members) contiguous chunks. The first
// total%N chunks take one extra element, so the split is a pure function of
// (total, N) and every rank computes byte-identical boundaries.
func (r *Ring) chunkBounds(total, index int) (int, int) {
	n := len(r.members)
	base, rem := total/n, total%n
	start := index*base + min(index, rem)
	size := base
	if index < rem {
		size++
	}
	return start, start + size
}

// AllReduce reduces values element-wise across every rank and leaves the same
// reduced vector in every rank's slice, using the standard two-phase ring:
// N-1 reduce-scatter steps, then N-1 all-gather steps. Each step sends one chunk
// to the successor and receives one chunk from the predecessor, so a rank moves
// 2·(N-1)/N of the vector instead of the N-1 copies a naive gather would.
//
// session is a 64-hex-character identifier the ranks already share (a job or
// attempt digest is the intended source). It scopes the inbox so two concurrent
// collectives cannot deliver into each other.
//
// Every wait is bounded and cancellable: the caller's context, the per-step
// timeout, and an abort from any rank all end a wait. A dead rank produces a
// RingFailure naming it, never a hang — and this rank tells the other survivors
// before it returns, so they do not each have to burn their own timeout.
func (r *Ring) AllReduce(ctx context.Context, session string, values []float64, op ReduceOp) error {
	if ctx == nil || !validDigest(session) || !op.valid() {
		return ErrInvalid
	}
	n := len(r.members)
	if len(values) < n || len(values) > MaxCollectiveValues {
		// Fewer values than ranks would give some rank an empty chunk, which is
		// not a frame this protocol can express. Refuse rather than silently
		// degrading to a different algorithm.
		return fmt.Errorf("%w: a ring all-reduce needs %d to %d values", ErrInvalid, n, MaxCollectiveValues)
	}
	if err := r.transport.authorized(); err != nil {
		return err
	}
	mailbox, err := r.transport.inbox.join(session, len(values), op)
	if err != nil {
		return err
	}
	defer r.transport.inbox.leave(session)
	err = r.allReduce(ctx, session, mailbox, values, op)
	if err != nil {
		r.reportFailure(ctx, session, err)
	}
	return err
}

func (r *Ring) allReduce(ctx context.Context, session string, mailbox *ringSession, values []float64, op ReduceOp) error {
	n := len(r.members)
	// Reduce-scatter. After N-1 steps rank i holds the fully reduced chunk
	// (i+1)%N and no other rank's partial sum for it.
	for step := range n - 1 {
		send := (r.rank - step + n) % n
		recv := (r.rank - step - 1 + n) % n
		if err := r.exchange(ctx, session, mailbox, collectivePhaseReduceScatter, step, send, recv, values, op, true); err != nil {
			return err
		}
	}
	// All-gather. Each step forwards the chunk this rank now holds definitively,
	// so after N-1 steps every rank holds every reduced chunk.
	for step := range n - 1 {
		send := (r.rank + 1 - step + n) % n
		recv := (r.rank - step + n) % n
		if err := r.exchange(ctx, session, mailbox, collectivePhaseAllGather, step, send, recv, values, op, false); err != nil {
			return err
		}
	}
	return nil
}

// exchange performs one step: post one chunk to the successor, then take the
// predecessor's chunk for that step out of the inbox. reduce selects whether the
// received chunk is combined into the local one (reduce-scatter) or replaces it
// (all-gather).
func (r *Ring) exchange(ctx context.Context, session string, mailbox *ringSession,
	phase uint8, step, sendIdx, recvIdx int, values []float64, op ReduceOp, reduce bool) error {
	n := len(r.members)
	successor := (r.rank + 1) % n
	predecessor := (r.rank - 1 + n) % n
	sendStart, sendEnd := r.chunkBounds(len(values), sendIdx)
	frame := collectiveFrame{
		kind: collectiveKindChunk, phase: phase, op: op,
		senderRank: r.rank, step: step, chunk: sendIdx,
		session: session, tenant: r.tenant, total: len(values),
		values: values[sendStart:sendEnd],
	}
	if err := r.transport.send(ctx, r.stepTimeout, frame, r.members[successor].Fingerprint); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The local caller went away. That is not the successor's fault and
			// must not be reported as its failure.
			return ctxErr
		}
		return &RingFailure{Rank: successor, Fingerprint: r.members[successor].Fingerprint,
			Reason: reasonUnreachable + ": " + err.Error(), err: ErrRankUnreachable}
	}
	received, err := r.transport.receive(ctx, mailbox, r.stepTimeout, phase, step)
	switch {
	case err == nil:
	case errors.Is(err, ErrTimeout):
		// Nothing arrived from the predecessor inside the step budget. Reporting
		// it as that rank's failure is what keeps the collective from hanging;
		// the alternative — waiting — is the deadlock this design refuses.
		return &RingFailure{Rank: predecessor, Fingerprint: r.members[predecessor].Fingerprint,
			Reason: reasonUnreachable + ": step timeout", err: ErrRankUnreachable}
	default:
		return err
	}
	start, end := r.chunkBounds(len(values), recvIdx)
	// The frame was authenticated by the transport, and its claimed sender rank
	// was already checked against the fingerprint that presented the client
	// certificate. These checks are the other half: that an authenticated member
	// sent the chunk this step actually expects. A mismatch leaves the local
	// vector untouched.
	if received.senderRank != predecessor || received.chunk != recvIdx ||
		received.total != len(values) || received.op != op || len(received.values) != end-start {
		return &RingFailure{Rank: predecessor, Fingerprint: r.members[predecessor].Fingerprint,
			Reason: reasonProtocol, err: ErrRankUnreachable}
	}
	if reduce {
		for i, v := range received.values {
			values[start+i] = op.apply(values[start+i], v)
		}
		return nil
	}
	copy(values[start:end], received.values)
	return nil
}

// reportFailure tells the survivors. A collective that dies silently leaves every
// other rank to wait out its own step timeout; an abort lets them all fail at
// once and name the same rank.
//
// It is deliberately not sent when the failure being reported *is* somebody
// else's abort: re-broadcasting a received abort would turn one failure into N²
// frames and could ring around forever.
//
// The notification runs on a context detached from the caller's, because the most
// common reason to be leaving early is that the caller's context was cancelled,
// and a cancelled context cannot carry the notice that this rank is gone.
func (r *Ring) reportFailure(ctx context.Context, session string, cause error) {
	if errors.Is(cause, ErrAborted) {
		return
	}
	rank, reason := r.rank, reasonProtocol
	var failure *RingFailure
	switch {
	case errors.As(cause, &failure):
		rank, reason = failure.Rank, reasonUnreachable
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		reason = reasonCancelled
	}
	notify, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	r.transport.broadcastAbort(notify, collectiveFrame{
		kind: collectiveKindAbort, senderRank: r.rank, failedRank: rank,
		session: session, tenant: r.tenant, reason: reason,
	})
}
