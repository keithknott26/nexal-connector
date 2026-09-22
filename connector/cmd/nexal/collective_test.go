package main

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"nexal/connector/internal/config"
	"nexal/connector/internal/pool"
)

// These tests cover the part of `nexal collective` that can be checked without a
// second machine: how RingOptions is built from identity, persisted membership
// and configured endpoints, and what the command refuses before it touches a
// socket. The end-to-end behaviour of the ring itself is tested in
// internal/pool, and a real two-process run over a link-local interface is
// recorded in the change's task report; neither is simulated here.

// enrolledPeer performs a real invite/prove/enroll cycle, because that is the
// only way a member enters a registry: Enroll verifies a signature over the
// invitation's random challenge. A hand-built Member would test a state the
// production code cannot reach.
func enrolledPeer(t *testing.T, registry *pool.Registry, roles ...pool.Role) pool.Identity {
	t.Helper()
	id, err := pool.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) == 0 {
		roles = []pool.Role{pool.Contributor}
	}
	invitation, err := registry.Invite(pool.DeviceID(id.PublicKey), roles, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := id.Prove(invitation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Enroll(proof); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBuildCollectivePlanDerivesRingOptionsFromRegistryAndStaticPeers(t *testing.T) {
	registry := pool.NewRegistry(nil)
	self, err := pool.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	peer := enrolledPeer(t, registry)
	peerID := pool.DeviceID(peer.PublicKey)

	plan, err := buildCollectivePlan(self, registry,
		[]config.StaticPeer{{Endpoint: "https://169.254.0.21:48442", Fingerprint: peerID}},
		"tenant-alpha", 5*time.Second)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.self != pool.DeviceID(self.PublicKey) {
		t.Fatalf("self fingerprint %q", plan.self)
	}
	if got := plan.options.AuthorizedPeers; len(got) != 1 || got[0] != peerID {
		t.Fatalf("authorized peers = %v", got)
	}
	if len(plan.options.Members) != 2 {
		t.Fatalf("members = %v", plan.options.Members)
	}
	for _, m := range plan.options.Members {
		if m.Tenant != "tenant-alpha" {
			t.Fatalf("member %s tenant = %q", m.Fingerprint, m.Tenant)
		}
		// pool requires this host's own entry to carry NO endpoint, because it
		// is served rather than dialed, and requires every peer to carry one.
		if m.Fingerprint == plan.self && m.Endpoint != "" {
			t.Fatalf("own entry carries endpoint %q", m.Endpoint)
		}
		if m.Fingerprint != plan.self && m.Endpoint == "" {
			t.Fatalf("peer %s carries no endpoint", m.Fingerprint)
		}
	}
	if plan.options.StepTimeout != 5*time.Second || plan.options.Registry != registry {
		t.Fatal("step timeout or registry not carried into options")
	}
	// The options must be acceptable to the real constructor, not merely
	// well-shaped: that is the whole purpose of building them. pool also requires
	// this host to be enrolled in its own registry, which is why the command runs
	// ensureSelfEnrolled first; asserting it here keeps that coupling visible.
	if _, err := ensureSelfEnrolled(registry, self); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.NewRing(plan.options); err != nil {
		t.Fatalf("NewRing rejected the built options: %v", err)
	}
}

// A configured endpoint is not authorization. An unenrolled or revoked peer must
// be reported and left out of the ring rather than passed to pool, where it would
// surface as an opaque ErrUnauthorized.
func TestBuildCollectivePlanExcludesUnenrolledAndRevokedPeers(t *testing.T) {
	registry := pool.NewRegistry(nil)
	self, err := pool.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	good := pool.DeviceID(enrolledPeer(t, registry).PublicKey)
	gone := pool.DeviceID(enrolledPeer(t, registry).PublicKey)
	if err := registry.Revoke(gone); err != nil {
		t.Fatal(err)
	}
	stranger, err := pool.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	unknown := pool.DeviceID(stranger.PublicKey)

	plan, err := buildCollectivePlan(self, registry, []config.StaticPeer{
		{Endpoint: "https://169.254.0.21:48442", Fingerprint: good},
		{Endpoint: "https://169.254.0.21:48443", Fingerprint: gone},
		{Endpoint: "https://169.254.0.21:48444", Fingerprint: unknown},
	}, "tenant-alpha", 0)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.options.Members) != 2 || plan.options.Members[1].Fingerprint != good {
		t.Fatalf("members = %v", plan.options.Members)
	}
	if len(plan.revoked) != 1 || plan.revoked[0] != gone {
		t.Fatalf("revoked = %v", plan.revoked)
	}
	if len(plan.unauthorized) != 1 || plan.unauthorized[0] != unknown {
		t.Fatalf("unauthorized = %v", plan.unauthorized)
	}
}

// A contributor role is required: manifest publication and the peer transport
// both demand it, and a ring member without it would fail inside newPeerPolicy.
func TestBuildCollectivePlanRequiresContributorRole(t *testing.T) {
	registry := pool.NewRegistry(nil)
	self, err := pool.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	admin := pool.DeviceID(enrolledPeer(t, registry, pool.Administrator).PublicKey)
	if _, err := buildCollectivePlan(self, registry,
		[]config.StaticPeer{{Endpoint: "https://169.254.0.21:48442", Fingerprint: admin}},
		"tenant-alpha", 0); err == nil {
		t.Fatal("a non-contributor member was accepted into the ring")
	}
}

func TestBuildCollectivePlanRejectsUnusableMembership(t *testing.T) {
	registry := pool.NewRegistry(nil)
	self, err := pool.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	selfID := pool.DeviceID(self.PublicKey)
	peer := pool.DeviceID(enrolledPeer(t, registry).PublicKey)

	// No registry at all.
	if _, err := buildCollectivePlan(self, nil, nil, "tenant-alpha", 0); err == nil {
		t.Fatal("a nil registry was accepted")
	}
	// No identity: a host that never ran `nexal identity`.
	if _, err := buildCollectivePlan(pool.Identity{}, registry, nil, "tenant-alpha", 0); err == nil {
		t.Fatal("a host with no peer identity was accepted")
	}
	// Enrolled peers but no configured endpoint: a ring needs somewhere to dial.
	if _, err := buildCollectivePlan(self, registry, nil, "tenant-alpha", 0); err == nil {
		t.Fatal("a ring with no dialable peer was accepted")
	}
	// A static entry for this host would collide with its own served entry.
	if _, err := buildCollectivePlan(self, registry,
		[]config.StaticPeer{{Endpoint: "https://169.254.0.21:48442", Fingerprint: selfID}},
		"tenant-alpha", 0); err == nil {
		t.Fatal("a static peer naming this host was accepted")
	}
	// A hand-edited duplicate must not become pool's duplicate-member error.
	if _, err := buildCollectivePlan(self, registry, []config.StaticPeer{
		{Endpoint: "https://169.254.0.21:48442", Fingerprint: peer},
		{Endpoint: "https://169.254.0.21:48443", Fingerprint: peer},
	}, "tenant-alpha", 0); err == nil {
		t.Fatal("a duplicated static peer was accepted")
	}
}

// Two Macs cabled together with no DHCP self-assign 169.254.0.0/16 addresses, so
// a link-local bind is the intended topology and must be accepted. A wildcard,
// a public address and a hostname stay refused.
func TestValidCollectiveListenAcceptsPrivateLoopbackAndLinkLocal(t *testing.T) {
	for _, listen := range []string{
		"169.254.0.21:48441", // Thunderbolt bridge / direct Ethernet, no DHCP.
		"127.0.0.1:48441",    // Single-host testing.
		"192.168.1.10:8443",  // RFC1918 LAN.
		"10.20.0.5:8443",     // RFC1918 across a VLAN.
		"[fd00::1]:8443",     // ULA.
		"[fe80::1]:8443",     // IPv6 link-local.
	} {
		if err := validCollectiveListen(listen); err != nil {
			t.Errorf("validCollectiveListen(%q) = %v, want accepted", listen, err)
		}
	}
	for _, listen := range []string{
		"0.0.0.0:48441",         // Wildcard bind: every interface, including public.
		"[::]:48441",            // IPv6 wildcard.
		"8.8.8.8:48441",         // Public route.
		"224.0.0.1:48441",       // Multicast.
		"mac-studio.local:8443", // Name: the peer transport performs no DNS lookup.
		"169.254.0.21",          // No port.
		"169.254.0.21:0",        // Port 0 cannot be dialed by a peer.
		"169.254.0.21:70000",    // Out of range.
		"",
	} {
		if err := validCollectiveListen(listen); err == nil {
			t.Errorf("validCollectiveListen(%q) = nil, want rejected", listen)
		}
	}
}

func TestParseReduceOpAndValues(t *testing.T) {
	for name, want := range map[string]pool.ReduceOp{
		"sum": pool.ReduceSum, "max": pool.ReduceMax, "min": pool.ReduceMin,
	} {
		got, err := parseReduceOp(name)
		if err != nil || got != want {
			t.Errorf("parseReduceOp(%q) = %v, %v", name, got, err)
		}
	}
	for _, name := range []string{"", "SUM", "mean", "product"} {
		if _, err := parseReduceOp(name); err == nil {
			t.Errorf("parseReduceOp(%q) accepted", name)
		}
	}

	values, err := parseCollectiveValues(" 1, 2.5 ,-3,4e2 ")
	if err != nil {
		t.Fatalf("parseCollectiveValues: %v", err)
	}
	if len(values) != 4 || values[1] != 2.5 || values[2] != -3 || values[3] != 400 {
		t.Fatalf("values = %v", values)
	}
	// NaN and infinity are refused: NaN propagates through addition and compares
	// false in max and min, so it would poison every rank's result silently.
	for _, raw := range []string{"", "   ", "1,,2", "1,two", "1,NaN", "1,Inf", "1,-Inf", "0x10"} {
		if _, err := parseCollectiveValues(raw); err == nil {
			t.Errorf("parseCollectiveValues(%q) accepted", raw)
		}
	}
	if _, err := parseCollectiveValues(strings.TrimSuffix(strings.Repeat("1,", maxCollectiveListValues+1), ",")); err == nil {
		t.Error("an over-long value list was accepted")
	}
	if math.IsNaN(values[0]) {
		t.Fatal("unreachable guard")
	}
}

// ensureSelfEnrolled exists because pool re-checks policy.enrolled(self) before
// every collective and on every inbound frame, and `nexal peers accept` only
// records the OTHER machine. It must be idempotent and must never resurrect a
// revoked host.
func TestEnsureSelfEnrolledIsIdempotentAndRespectsRevocation(t *testing.T) {
	registry := pool.NewRegistry(nil)
	self, err := pool.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	created, err := ensureSelfEnrolled(registry, self)
	if err != nil || !created {
		t.Fatalf("first enrollment: created=%v err=%v", created, err)
	}
	member, ok := registry.Member(pool.DeviceID(self.PublicKey))
	if !ok || member.RevokedAt != nil {
		t.Fatal("this host is not an enrolled member of its own registry")
	}
	created, err = ensureSelfEnrolled(registry, self)
	if err != nil || created {
		t.Fatalf("second enrollment: created=%v err=%v", created, err)
	}
	if err := registry.Revoke(pool.DeviceID(self.PublicKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureSelfEnrolled(registry, self); err == nil {
		t.Fatal("a revoked host was silently re-enrolled")
	}
}

// waitForPeers is a startup barrier, not a health check: whichever rank starts
// first must not report the other as a failed rank simply for being slower to
// bind. It must also stay bounded when a peer never appears.
func TestWaitForPeersIsBoundedAndSkipsSelf(t *testing.T) {
	self := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	// Only this host in the ring: nothing to dial, so it returns at once.
	if err := waitForPeers(context.Background(),
		[]pool.RingMember{{Fingerprint: self}}, self, time.Second); err != nil {
		t.Fatalf("self-only ring: %v", err)
	}
	// Port 1 on loopback is not listening, so this must give up on the deadline
	// rather than block.
	started := time.Now()
	err := waitForPeers(context.Background(), []pool.RingMember{
		{Fingerprint: self},
		{Fingerprint: other, Endpoint: "https://127.0.0.1:1"},
	}, self, 200*time.Millisecond)
	if err == nil {
		t.Fatal("an unreachable peer was reported ready")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("wait was not bounded: %s", elapsed)
	}
	// A cancelled context ends the wait immediately with the context's error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForPeers(ctx, []pool.RingMember{
		{Fingerprint: self},
		{Fingerprint: other, Endpoint: "https://127.0.0.1:1"},
	}, self, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait returned %v", err)
	}
}

// The command must refuse an unusable invocation before it loads an identity,
// binds a socket or dials anything, and must never echo an argument back.
func TestCollectiveCommandRejectsUnsafeFlags(t *testing.T) {
	for _, args := range [][]string{
		{"collective"},
		{"collective", "--config", "relative-path"},
		{"collective", "--config", "/tmp/nexal-absent/config.json"},
		{"collective", "--config", "/tmp/nexal-absent/config.json", "--tenant", "t"},
		// Wildcard bind.
		{"collective", "--config", "/tmp/nexal-absent/config.json", "--tenant", "t",
			"--listen", "0.0.0.0:48441", "--session", strings.Repeat("a", 64), "--values", "1,2"},
		// Public bind.
		{"collective", "--config", "/tmp/nexal-absent/config.json", "--tenant", "t",
			"--listen", "8.8.8.8:48441", "--session", strings.Repeat("a", 64), "--values", "1,2"},
		// Unknown reduction.
		{"collective", "--config", "/tmp/nexal-absent/config.json", "--tenant", "t",
			"--listen", "127.0.0.1:48441", "--session", strings.Repeat("a", 64), "--values", "1,2", "--op", "mean"},
		// Timeouts outside the bound.
		{"collective", "--config", "/tmp/nexal-absent/config.json", "--tenant", "t",
			"--listen", "127.0.0.1:48441", "--session", strings.Repeat("a", 64), "--values", "1,2", "--timeout", "1h"},
		{"collective", "--config", "/tmp/nexal-absent/config.json", "--tenant", "t",
			"--listen", "127.0.0.1:48441", "--session", strings.Repeat("a", 64), "--values", "1,2", "--step-timeout", "0s"},
		// Unknown flag, carrying a value that must not be echoed.
		{"collective", "--config", "/tmp/nexal-absent/config.json", "--secret", "must-not-echo"},
	} {
		err := run(context.Background(), args)
		if err == nil {
			t.Fatalf("accepted unusable invocation: %v", args)
		}
		if strings.Contains(err.Error(), "must-not-echo") {
			t.Fatal("flag value echoed in the error")
		}
	}
}
