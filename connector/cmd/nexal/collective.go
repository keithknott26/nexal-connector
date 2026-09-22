package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/peerregistry"
	"nexal/connector/internal/pool"
)

// `nexal collective` runs one ring all-reduce across this host and its enrolled
// peers.
//
// WHY THIS COMMAND EXISTS: internal/pool/collective.go implements the ring
// collective HARDENING-PLAN §39.2 specifies — a reduce-scatter followed by an
// all-gather over the fingerprint-pinned mutual-TLS peer transport — and it was
// fully tested, but NewRing had no caller anywhere outside the package's own
// tests. Every input it needs already existed on disk and in the CLI and nothing
// assembled them: this host's identity (internal/peeridentity), persisted
// membership (internal/peerregistry, written by `nexal peers accept`), and the
// peers' dial endpoints (`nexal static-peers add`). This command is that
// assembly and nothing more. It adds no transport, no protocol and no authority.
//
// WHAT IT IS NOT, stated here rather than left to be inferred. An all-reduce is
// a tensor reduction: N ranks each hold a vector of float64 and every rank ends
// holding the element-wise reduction of all of them. That is the whole of it.
// There is NO workload runtime, NO model execution, NO MLX and NO scheduler
// behind this command, and the memory of two Macs does not become one pool that
// the OS can see — nothing here changes how much RAM either machine reports or
// can allocate. It is the collective primitive a distributed runtime would need,
// exercised end to end, not a distributed runtime.
//
// AUTHORIZATION IS THE REGISTRY'S, NOT THIS COMMAND'S. Ring membership is drawn
// from enrolled, unrevoked contributor members of the persisted registry. A
// static peer entry supplies an ADDRESS and the fingerprint to PIN; it cannot
// add a rank. A configured peer that is not enrolled is reported as configured
// and unauthorized and is left out of the ring, exactly as §30.2 requires.

const (
	// maxCollectiveWait bounds --timeout and --peer-wait so a mistyped flag
	// cannot turn this command into an unbounded blocking process.
	maxCollectiveWait = 10 * time.Minute
	// maxCollectiveListValues bounds the --values list independently of
	// pool.MaxCollectiveValues: this is a hand-typed or script-generated CLI
	// argument, and a 262144-element argv is a mistake rather than an input.
	maxCollectiveListValues = 4096
)

// collectivePlan is the fully resolved, side-effect-free description of one run:
// the ring options pool.NewRing will be given, plus the human-facing facts the
// JSON output has to report. It is built by buildCollectivePlan, which touches
// no network and no clock, so the option-building and validation rules are
// testable without two machines.
type collectivePlan struct {
	options pool.RingOptions
	self    string
	// unauthorized lists configured static peers that are not enrolled here.
	// They are reported rather than dropped silently, because "I added the peer
	// and nothing happened" is the failure this output has to prevent.
	unauthorized []string
	// revoked lists configured peers whose membership was revoked. Kept
	// separate from unauthorized: one is "not enrolled yet", the other is "was
	// removed on purpose", and confusing them would invite an operator to
	// re-enroll a machine they meant to exclude.
	revoked []string
}

// buildCollectivePlan derives ring options from this host's identity, the
// persisted registry and the configured static peers.
//
// The registry is the authority. AuthorizedPeers is its enrolled, unrevoked
// contributor set minus this host, which is the closest thing a connector
// without a coordinator-supplied list has to one — and it is strictly narrower
// than the configuration, never wider. Members is that set intersected with the
// static peers that supply an endpoint, plus this host's own entry, which by
// pool's rule carries no endpoint because it is served rather than dialed.
func buildCollectivePlan(id pool.Identity, registry *pool.Registry,
	peers []config.StaticPeer, tenant string, step time.Duration) (collectivePlan, error) {
	if registry == nil {
		return collectivePlan{}, errors.New("a peer registry is required")
	}
	self := pool.DeviceID(id.PublicKey)
	if self == "" {
		return collectivePlan{}, errors.New("this host has no peer identity; run `nexal identity` first")
	}
	// Membership state, indexed by fingerprint, so a configured peer can be
	// classified once as authorized, revoked or unknown.
	authorized := map[string]bool{}
	revoked := map[string]bool{}
	for _, m := range registry.Snapshot() {
		if m.RevokedAt != nil {
			revoked[m.DeviceID] = true
			continue
		}
		contributor := false
		for _, role := range m.Roles {
			if role == pool.Contributor {
				contributor = true
			}
		}
		if contributor && m.DeviceID != self {
			authorized[m.DeviceID] = true
		}
	}

	plan := collectivePlan{self: self}
	members := []pool.RingMember{{Fingerprint: self, Tenant: tenant}}
	seen := map[string]bool{self: true}
	var allowlist []string
	for _, p := range peers {
		switch {
		case p.Fingerprint == self:
			// A static entry for this host is an operator mistake that would
			// otherwise become "this host's own ring entry must carry an
			// endpoint" deep inside pool. Say so here instead.
			return collectivePlan{}, errors.New("a static peer names this host's own fingerprint; remove it with `nexal static-peers remove`")
		case revoked[p.Fingerprint]:
			plan.revoked = append(plan.revoked, p.Fingerprint)
		case !authorized[p.Fingerprint]:
			plan.unauthorized = append(plan.unauthorized, p.Fingerprint)
		case seen[p.Fingerprint]:
			// config.AddStaticPeer already rejects duplicates; this keeps a
			// hand-edited file from producing pool's duplicate-member error.
			return collectivePlan{}, fmt.Errorf("static peers list %s twice", p.Fingerprint)
		default:
			seen[p.Fingerprint] = true
			allowlist = append(allowlist, p.Fingerprint)
			members = append(members, pool.RingMember{
				Fingerprint: p.Fingerprint, Tenant: tenant, Endpoint: p.Endpoint,
			})
		}
	}
	if len(allowlist) == 0 {
		return collectivePlan{}, errors.New("no enrolled peer has a configured endpoint: " +
			"enroll with `nexal peers invite|prove|accept`, then `nexal static-peers add`")
	}
	plan.options = pool.RingOptions{
		Identity: id, Registry: registry, Tenant: tenant,
		AuthorizedPeers: allowlist, Members: members, StepTimeout: step,
	}
	return plan, nil
}

// validCollectiveListen applies the listener rule through the SAME function that
// validates a peer's endpoint, by building the endpoint this host would be
// dialed at. That is deliberate: if the address a rank binds and the address its
// peers dial were checked by two different rules, one could accept a bind the
// other would refuse and the operator would see a ring that half-forms.
//
// Link-local is accepted, and that is the point for the intended topology. Two
// Macs joined by a Thunderbolt bridge or a straight Ethernet run have no DHCP
// server between them and self-assign 169.254.0.0/16 addresses, so a rule that
// refused them would refuse exactly the setup this collective is for.
func validCollectiveListen(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || host == "" || port == "" {
		return errors.New("--listen must be numeric ip:port, e.g. 169.254.0.21:8443 or 127.0.0.1:8443")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("--listen port must be 1-65535")
	}
	if ip := net.ParseIP(host); ip == nil {
		return errors.New("--listen must be a literal IP address, not a hostname: the peer transport performs no DNS lookup")
	}
	if !config.ValidPeerEndpoint("https://" + net.JoinHostPort(host, port)) {
		return errors.New("--listen must be a private, loopback or link-local unicast address; a wildcard or public bind is refused")
	}
	return nil
}

// parseReduceOp maps the flag to pool's operation. Every rank must agree: the
// operation travels in every frame and pool refuses a frame whose operation
// differs from the local one, so a mismatch is a named error rather than a
// result that is neither sum nor max.
func parseReduceOp(name string) (pool.ReduceOp, error) {
	switch name {
	case "sum":
		return pool.ReduceSum, nil
	case "max":
		return pool.ReduceMax, nil
	case "min":
		return pool.ReduceMin, nil
	default:
		return 0, errors.New("--op must be sum, max or min")
	}
}

// parseCollectiveValues parses this rank's contribution.
//
// NaN and infinity are refused. A reduction containing NaN silently poisons
// every element of every rank's result — NaN propagates through addition and
// compares false in max and min — so it would produce a green run whose numbers
// mean nothing, which is worse than an error.
func parseCollectiveValues(raw string) ([]float64, error) {
	fields := strings.Split(strings.TrimSpace(raw), ",")
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("--values is required: this rank's contribution, e.g. --values 1,2,3,4")
	}
	if len(fields) > maxCollectiveListValues {
		return nil, fmt.Errorf("--values takes at most %d elements", maxCollectiveListValues)
	}
	out := make([]float64, 0, len(fields))
	for _, field := range fields {
		v, err := strconv.ParseFloat(strings.TrimSpace(field), 64)
		if err != nil {
			return nil, errors.New("--values must be a comma-separated list of finite decimal numbers")
		}
		// Rejects NaN and ±Inf: v != v is true only for NaN.
		if v != v || v > 1e308 || v < -1e308 {
			return nil, errors.New("--values must be finite: NaN or infinity would silently poison every rank's result")
		}
		out = append(out, v)
	}
	return out, nil
}

// ensureSelfEnrolled guarantees this host is an enrolled contributor in its own
// registry, which pool requires: the ring transport re-checks
// policy.enrolled(self) before every collective and on every inbound frame, so a
// host absent from its own membership file cannot serve or send.
//
// `nexal peers accept` enrolls the OTHER machine, so nothing had ever recorded
// this one. The enrollment performed here is a real one, not a bypass: the local
// registry mints an invitation for this host's own fingerprint, this host signs
// that invitation's random challenge with its own private key, and Enroll
// verifies the signature and that the key hashes to the named fingerprint. No
// path here can enroll anything other than the key this host already holds.
//
// It returns true when it wrote a new membership record, so the caller can
// persist it and say so.
func ensureSelfEnrolled(registry *pool.Registry, id pool.Identity) (bool, error) {
	self := pool.DeviceID(id.PublicKey)
	if member, ok := registry.Member(self); ok {
		if member.RevokedAt != nil {
			// Refused rather than re-enrolled: an owner who revoked this host
			// has to say so explicitly, and silently undoing it from a
			// collective run would make revocation meaningless.
			return false, errors.New("this host's own membership is revoked; the collective will not re-enroll it automatically")
		}
		return false, nil
	}
	invitation, err := registry.Invite(self, []pool.Role{pool.Contributor}, time.Minute)
	if err != nil {
		return false, fmt.Errorf("enroll this host in its own registry: %w", err)
	}
	proof, err := id.Prove(invitation)
	if err != nil {
		return false, fmt.Errorf("sign this host's own enrollment: %w", err)
	}
	if _, err := registry.Enroll(proof); err != nil {
		return false, fmt.Errorf("enroll this host in its own registry: %w", err)
	}
	return true, nil
}

// waitForPeers dials each peer's endpoint until the TCP listener answers or the
// deadline passes. It is a startup barrier, not a health check: two ranks are
// started by hand or by a launch script and one will always be first, and the
// first rank's opening frame would otherwise be refused by the kernel and
// reported as a failed rank before the second rank had bound its socket.
//
// It deliberately performs a plain TCP dial and no TLS handshake. Reachability
// is all this needs to know, and attempting authentication here would mean a
// second, weaker copy of a trust decision the transport already makes correctly
// on every connection.
func waitForPeers(ctx context.Context, members []pool.RingMember, self string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for _, m := range members {
		if m.Fingerprint == self || m.Endpoint == "" {
			continue
		}
		address := strings.TrimPrefix(m.Endpoint, "https://")
		for {
			conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", address)
			if err == nil {
				_ = conn.Close()
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !time.Now().Before(deadline) {
				return fmt.Errorf("peer %s at %s did not accept a connection within %s", m.Fingerprint, address, wait)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return nil
}

// transportLog captures whatever net/http's server writes to the standard
// logger while this rank is serving, instead of letting it interleave with the
// command's output.
//
// It is captured rather than discarded. The CLI contract is that stdout carries
// one JSON object and stderr carries one JSON error, and an http.Server whose
// ErrorLog is nil writes straight to the standard logger — so a rejected
// handshake would otherwise appear as a bare log line in the middle of a
// machine-readable stream. Discarding it would hide a real failure, so the lines
// are reported inside the JSON instead. Writes arrive from the server's own
// goroutines, hence the lock and the cap.
type transportLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *transportLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() < 8<<10 {
		return l.buf.Write(p)
	}
	return len(p), nil
}

func (l *transportLog) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range strings.Split(l.buf.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func collectiveCommand(ctx context.Context, args []string) error {
	f, path, err := flags("collective")
	if err != nil {
		return err
	}
	tenant := f.String("tenant", "", "mandatory tenant scope label every rank must agree on")
	listen := f.String("listen", "", "numeric private, loopback or link-local ip:port to serve this rank on")
	session := f.String("session", "", "64-hex collective session identifier, identical on every rank")
	values := f.String("values", "", "this rank's contribution as comma-separated finite numbers")
	op := f.String("op", "sum", "reduction applied element-wise: sum, max or min")
	step := f.Duration("step-timeout", 30*time.Second, "per-step send/receive ceiling, at most 10m")
	wait := f.Duration("peer-wait", 30*time.Second, "how long to wait for each peer's listener before starting, at most 10m")
	budget := f.Duration("timeout", 2*time.Minute, "overall ceiling for the collective, at most 10m")
	if err := parse(f, args, path); err != nil {
		return err
	}
	if f.NArg() != 0 || *tenant == "" || *listen == "" || *session == "" || *values == "" {
		return errors.New("usage: nexal collective --tenant label --listen ip:port --session 64-hex --values 1,2,3,4 " +
			"[--op sum|max|min] [--step-timeout 30s] [--peer-wait 30s] [--timeout 2m] [--config absolute-path]")
	}
	if *step <= 0 || *step > maxCollectiveWait || *wait <= 0 || *wait > maxCollectiveWait ||
		*budget <= 0 || *budget > maxCollectiveWait {
		return fmt.Errorf("--step-timeout, --peer-wait and --timeout must be positive and at most %s", maxCollectiveWait)
	}
	reduce, err := parseReduceOp(*op)
	if err != nil {
		return err
	}
	contribution, err := parseCollectiveValues(*values)
	if err != nil {
		return err
	}
	if err := validCollectiveListen(*listen); err != nil {
		return err
	}

	id, err := selfIdentity(ctx, *path)
	if err != nil {
		return err
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}

	// The registry is read, possibly amended with this host's own membership,
	// and written back under ONE hold of the configuration lock, following
	// `nexal peers`: a concurrent `peers accept` that loaded the same file would
	// otherwise write its own result over this one, dropping a peer.
	registry, registryPath, unlock, err := openRegistry(*path)
	if err != nil {
		return err
	}
	enrolledSelf, err := ensureSelfEnrolled(registry, id)
	if err != nil {
		unlock()
		return err
	}
	if enrolledSelf {
		if err := peerregistry.Save(registryPath, registry); err != nil {
			unlock()
			return err
		}
	}
	plan, err := buildCollectivePlan(id, registry, c.StaticPeers, *tenant, *step)
	// The lock is released before the collective runs. It guards the membership
	// FILE, and a collective can run for minutes: holding it across the network
	// phase would block `peers revoke` for the duration, which is the one
	// command an owner may need most urgently. The ring re-checks the registry
	// per request anyway, so a revocation still takes effect mid-run.
	unlock()
	if err != nil {
		return err
	}

	ring, err := pool.NewRing(plan.options)
	if err != nil {
		return err
	}
	defer func() { _ = ring.Close() }()

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("bind %s: %w", *listen, err)
	}
	// Serve validates the bound address itself before taking ownership, so a
	// listener this command mis-selected is refused there too rather than
	// trusted because it got this far.
	captured := &transportLog{}
	previousLog := log.Writer()
	log.SetOutput(captured)
	defer log.SetOutput(previousLog)
	served := make(chan error, 1)
	go func() { served <- ring.Serve(listener) }()

	run, cancel := context.WithTimeout(ctx, *budget)
	defer cancel()
	if err := waitForPeers(run, ring.Members(), plan.self, *wait); err != nil {
		return err
	}

	result := make([]float64, len(contribution))
	copy(result, contribution)
	started := time.Now()
	reduceErr := ring.AllReduce(run, *session, result, reduce)
	elapsed := time.Since(started)

	// Shut the server down before reporting, so the process does not exit with
	// a peer's connection half-open and so a survivor's abort has a chance to
	// be delivered.
	stop, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer stopCancel()
	_ = ring.Shutdown(stop)
	select {
	case <-served:
	case <-time.After(3 * time.Second):
	}
	if reduceErr != nil {
		return reduceErr
	}

	members := make([]map[string]any, 0, ring.Size())
	for rank, m := range ring.Members() {
		row := map[string]any{"rank": rank, "deviceId": m.Fingerprint, "self": m.Fingerprint == plan.self}
		if m.Endpoint != "" {
			row["endpoint"] = m.Endpoint
		}
		members = append(members, row)
	}
	out := map[string]any{
		"tenant":      ring.Tenant(),
		"session":     *session,
		"op":          *op,
		"rank":        ring.Rank(),
		"size":        ring.Size(),
		"listen":      listener.Addr().String(),
		"members":     members,
		"contributed": contribution,
		"result":      result,
		"elapsedMs":   elapsed.Milliseconds(),
		"successor":   ring.Successor().Fingerprint,
		"predecessor": ring.Predecessor().Fingerprint,
		"scope":       "an all-reduce is a tensor reduction: every rank now holds the element-wise reduction of every rank's vector",
		"notCompute":  "no workload, model or MLX runtime ran, and no memory was pooled: this host's usable RAM is unchanged",
		"transport":   "TLS 1.3 mutual authentication with each peer's device fingerprint pinned; membership came from the persisted registry, not from the network",
	}
	if lines := captured.lines(); len(lines) > 0 {
		// One "TLS handshake error ... EOF" per peer is expected and benign: it is
		// this rank's own readiness probe, a plain TCP connect that closes without
		// negotiating TLS. Reported rather than filtered, because a filter keyed
		// on that text would also swallow a genuine handshake rejection.
		out["transportLog"] = lines
		out["transportLogNote"] = "one TLS handshake EOF per peer is this rank's own TCP readiness probe, not a rejected peer"
	}
	if enrolledSelf {
		out["enrolledSelf"] = "this host was not in its own peer registry and was enrolled with its own signed key so it could serve and send"
	}
	if len(plan.unauthorized) > 0 {
		out["configuredNotAuthorized"] = plan.unauthorized
	}
	if len(plan.revoked) > 0 {
		out["configuredRevoked"] = plan.revoked
	}
	return emit(out)
}
