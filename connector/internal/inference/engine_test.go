package inference

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const gb = uint64(1) << 30

type fakeSource struct {
	hosts  []Host
	links  []Link
	extras StatusExtras
	herr   error
	// exact: use extras as given instead of defaulting to a reachable connector.
	exact bool
}

func (f fakeSource) Hosts(context.Context) ([]Host, error) { return f.hosts, f.herr }
func (f fakeSource) Links(context.Context) ([]Link, error) { return f.links, nil }
func (f fakeSource) Extras() StatusExtras                  { return f.extras }

type fakeRuntime struct{ p RuntimeProbe }

func (f fakeRuntime) Probe(context.Context) RuntimeProbe { return f.p }

var healthy = RuntimeProbe{State: RuntimeHealthy, Detail: "ok", RuntimeVersion: "0.2.0", MLXVersion: "0.29.3", MLXLMVersion: "0.28.4", DistributedExecution: "disabled"}

func selfMac(totalGiB, availGiB, capGiB, reserveGiB uint64) Host {
	return Host{ID: "self", Name: "MacBook", IsSelf: true, Online: true, Chip: "Apple M4 Max", OS: "macOS 26.2",
		TotalMemoryBytes: totalGiB * gb, AvailableMemoryBytes: availGiB * gb, MemoryKnown: true,
		AdmissionKnown: true, ApprovedMemoryBytes: capGiB * gb, OwnerReserveBytes: reserveGiB * gb,
		DiskFreeBytes: 500 * gb, DiskKnown: true}
}

func peerMac(id string, totalGiB, availGiB uint64, online bool) Host {
	return Host{ID: id, Name: "Mini-" + id, Online: online, Chip: "Apple M4", OS: "macOS 26.2",
		TotalMemoryBytes: totalGiB * gb, AvailableMemoryBytes: availGiB * gb, MemoryKnown: true,
		DiskFreeBytes: 300 * gb, DiskKnown: true}
}

func goodLink(id string) Link {
	return Link{PeerID: id, Lifecycle: "connected", Path: "direct", PathLabel: "Direct", DirectVia: "lan",
		LatencyMS: 2, LatencyKnown: true, BandwidthMbps: 900, BandwidthKnown: true, PQ: "protected"}
}

func run(t *testing.T, src fakeSource, rt RuntimeProbe) Report {
	t.Helper()
	cat, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if !src.exact {
		src.extras = StatusExtras{Reachable: true, MeshPQ: "protected"}
	}
	e := Engine{Hosts: src, Links: src, Runtime: fakeRuntime{rt}, Catalog: cat,
		Now: func() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }}
	rep, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func model(t *testing.T, r Report, id string) ModelVerdict {
	t.Helper()
	for _, m := range r.Models {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("model %s not in report", id)
	return ModelVerdict{}
}

func hasBlocker(r Report, code string) bool {
	for _, b := range r.Blockers {
		if b.Code == code {
			return true
		}
	}
	return false
}

func TestScenarios(t *testing.T) {
	small := []Host{selfMac(24, 20, 16, 2), peerMac("a", 24, 20, true), peerMac("b", 24, 20, true)}
	smallLinks := []Link{goodLink("a"), goodLink("b")}
	relay := goodLink("b")
	relay.Path, relay.PathLabel, relay.DirectVia, relay.PathFlapsLastHour = "relay", "Relay (Frankfurt)", "", 7

	tests := []struct {
		name  string
		src   fakeSource
		rt    RuntimeProbe
		check func(t *testing.T, r Report)
	}{
		{
			name: "single big host fits everything",
			src:  fakeSource{hosts: []Host{selfMac(128, 100, 96, 8)}},
			rt:   healthy,
			check: func(t *testing.T, r Report) {
				for _, m := range r.Models {
					if m.Verdict != VerdictRunsNow || m.HostName != "MacBook" {
						t.Errorf("%s: verdict %s host %s", m.ID, m.Verdict, m.HostName)
					}
				}
				if r.Recommendation.ModelID != "qwen3-32b-q8" || !r.Recommendation.RunnableNow {
					t.Errorf("recommendation = %+v", r.Recommendation)
				}
				if r.Sharing.SingleHostExecution != "available" || r.Sharing.MultiHostExecution != "unavailable" {
					t.Errorf("sharing = %+v", r.Sharing)
				}
				if hasBlocker(r, "multi-host-unavailable") {
					t.Error("no model is sharded, so no multi-host blocker")
				}
			},
		},
		{
			name: "two small peers fit only sharded",
			src:  fakeSource{hosts: small, links: smallLinks},
			rt:   healthy,
			check: func(t *testing.T, r Report) {
				m := model(t, r, "qwen3-14b-q8")
				if m.Verdict != VerdictFitsShardedOnly || len(m.Layout) < 2 {
					t.Fatalf("14b-q8 = %+v", m)
				}
				if m.LayoutLinksOK == nil || !*m.LayoutLinksOK {
					t.Errorf("good links should give layoutLinksOk=true: %+v", m.LayoutLinksOK)
				}
				seen := map[string]bool{}
				for _, rk := range m.Layout {
					if seen[rk.MachineID] {
						t.Errorf("machine %s holds two ranks", rk.MachineID)
					}
					seen[rk.MachineID] = true
					if rk.RequiredBytes > rk.UsableBytes {
						t.Errorf("rank %d needs %d > usable %d", rk.Rank, rk.RequiredBytes, rk.UsableBytes)
					}
				}
				if got := model(t, r, "qwen3-14b-q4").Verdict; got != VerdictRunsNow {
					t.Errorf("14b-q4 fits self alone, got %s", got)
				}
				if r.Sharing.Statement != "Sharing across peers: planned layout fits, but multi-host execution is not available yet; single-host run is available." {
					t.Errorf("statement = %q", r.Sharing.Statement)
				}
				if !hasBlocker(r, "multi-host-unavailable") {
					t.Error("expected multi-host-unavailable info blocker")
				}
				// Recommendation must never be a sharded-only model.
				if r.Recommendation.ModelID == "qwen3-14b-q8" || r.Recommendation.ModelID == "qwen3-32b-q4" {
					t.Errorf("recommended a non-runnable model: %s", r.Recommendation.ModelID)
				}
			},
		},
		{
			name: "nothing fits",
			src:  fakeSource{hosts: []Host{selfMac(4, 2, 1, 1)}},
			rt:   healthy,
			check: func(t *testing.T, r Report) {
				for _, m := range r.Models {
					if m.Verdict != VerdictDoesNotFit {
						t.Errorf("%s: %s", m.ID, m.Verdict)
					}
				}
				if r.Recommendation.ModelID != "" || r.Recommendation.RunnableNow {
					t.Errorf("recommendation = %+v", r.Recommendation)
				}
				if !strings.Contains(r.Headline, "No catalog model fits") {
					t.Errorf("headline = %q", r.Headline)
				}
				if r.Sharing.Statement != statementNoShard {
					t.Errorf("statement = %q", r.Sharing.Statement)
				}
			},
		},
		{
			name: "relay-only unstable link",
			src:  fakeSource{hosts: small, links: []Link{goodLink("a"), relay}},
			rt:   healthy,
			check: func(t *testing.T, r Report) {
				var lb LinkReport
				for _, l := range r.Links {
					if l.PeerID == "b" {
						lb = l
					}
				}
				if lb.Quality != "poor" || !lb.Unstable || lb.Path != "relay" || lb.PathFlapsLastHour != 7 {
					t.Errorf("link b = %+v", lb)
				}
				if !hasBlocker(r, "link-unstable") {
					t.Error("expected link-unstable blocker")
				}
				m := model(t, r, "qwen3-14b-q8")
				if m.Verdict != VerdictFitsShardedOnly || m.LayoutLinksOK == nil || *m.LayoutLinksOK {
					t.Errorf("layout must be flagged as having a poor link: %+v", m)
				}
			},
		},
		{
			name: "runtime not provisioned",
			src:  fakeSource{hosts: []Host{selfMac(64, 50, 48, 4)}},
			rt:   RuntimeProbe{State: RuntimeNotConfigured, Detail: "never provisioned"},
			check: func(t *testing.T, r Report) {
				for _, m := range r.Models {
					if m.Verdict == VerdictRunsNow {
						t.Errorf("%s must not run with no runtime", m.ID)
					}
				}
				if model(t, r, "qwen3-32b-q4").Verdict != VerdictFitsSingleBlocked {
					t.Errorf("32b-q4 should fit but be blocked: %+v", model(t, r, "qwen3-32b-q4"))
				}
				if r.Recommendation.RunnableNow || r.Recommendation.ModelID == "" {
					t.Errorf("recommendation = %+v", r.Recommendation)
				}
				if !hasBlocker(r, "runtime-not-configured") || r.Sharing.SingleHostExecution != "blocked" {
					t.Errorf("blockers=%v sharing=%+v", r.Blockers, r.Sharing)
				}
				if r.Blockers[0].Severity != SeverityBlocker || r.Blockers[0].Fix == "" {
					t.Errorf("first blocker must be a blocker with a fix: %+v", r.Blockers[0])
				}
			},
		},
		{
			name: "peer offline is listed but not used",
			src: fakeSource{hosts: []Host{selfMac(24, 20, 16, 2), peerMac("a", 64, 60, false)},
				links: []Link{{PeerID: "a", Lifecycle: "offline", Path: "unknown", PQ: "protected"}}},
			rt: healthy,
			check: func(t *testing.T, r Report) {
				if len(r.Machines) != 2 || r.Machines[1].Eligible || r.Machines[1].Online {
					t.Fatalf("machines = %+v", r.Machines)
				}
				if !strings.Contains(strings.Join(r.Machines[1].Reasons, " "), "Offline") {
					t.Errorf("reasons = %v", r.Machines[1].Reasons)
				}
				if !hasBlocker(r, "peer-offline") {
					t.Error("expected peer-offline")
				}
				if r.Links[0].Quality != "unusable" {
					t.Errorf("offline link quality = %s", r.Links[0].Quality)
				}
				if got := model(t, r, "qwen3-32b-q4").Verdict; got != VerdictDoesNotFit {
					t.Errorf("32b-q4 = %s (the offline Mac must not host it)", got)
				}
			},
		},
		{
			name: "peer without verified PQ is excluded",
			src: fakeSource{hosts: []Host{selfMac(24, 20, 16, 2), peerMac("a", 24, 20, true)},
				links: []Link{func() Link { l := goodLink("a"); l.PQ, l.PQReason = "degraded", "evidence-stale"; return l }()}},
			rt: healthy,
			check: func(t *testing.T, r Report) {
				if r.Machines[1].Eligible || !hasBlocker(r, "peer-pq-unverified") {
					t.Errorf("machine=%+v blockers=%v", r.Machines[1], r.Blockers)
				}
			},
		},
		{
			name: "owner cap, not hardware, limits fit",
			src:  fakeSource{hosts: []Host{selfMac(64, 60, 4, 1)}},
			rt:   healthy,
			check: func(t *testing.T, r Report) {
				m := model(t, r, "qwen3-8b-q4") // ~6.8 GiB estimate > 4 GiB cap, < 8 GiB maximum
				if m.Verdict != VerdictDoesNotFit || !m.FitsIfCapRaised {
					t.Errorf("8b-q4 = %+v", m)
				}
				if !hasBlocker(r, "owner-memory-cap") {
					t.Error("expected owner-memory-cap blocker")
				}
				big := model(t, r, "qwen3-32b-q4")
				if big.FitsIfCapRaised {
					t.Error("a model over the 8 GiB policy maximum cannot be fixed by raising the cap")
				}
				if !hasBlocker(r, "policy-cap-maximum") {
					t.Error("expected policy-cap-maximum blocker")
				}
				if model(t, r, "qwen3-1.7b-q4").Verdict != VerdictRunsNow {
					t.Error("1.7b-q4 (about 2.7 GiB) fits in a 4 GiB cap")
				}
			},
		},
		{
			name: "connector unreachable",
			src:  fakeSource{hosts: []Host{{ID: "this-mac", Name: "MacBook", IsSelf: true, Online: true, Chip: "Apple M4", TotalMemoryBytes: 16 * gb}}, extras: StatusExtras{Reachable: false}, exact: true},
			rt:   healthy,
			check: func(t *testing.T, r Report) {
				if !hasBlocker(r, "connector-unreachable") || r.Machines[0].Eligible || r.Machines[0].AdmissionSource != "unavailable" {
					t.Errorf("machine=%+v blockers=%v", r.Machines[0], r.Blockers)
				}
				if r.Recommendation.ModelID != "" {
					t.Errorf("must not recommend without policy: %+v", r.Recommendation)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, tc.src, tc.rt)
			tc.check(t, r)
			// Invariants that hold for every scenario.
			if r.SchemaVersion != SchemaVersion || r.GeneratedAt != "2026-10-09T12:00:00Z" {
				t.Errorf("header = %d %s", r.SchemaVersion, r.GeneratedAt)
			}
			if r.Sharing.MultiHostExecution != "unavailable" {
				t.Error("multi-host execution must be reported unavailable")
			}
			if !hasBlocker(r, "catalog-not-pinned") {
				t.Error("catalog-not-pinned must be reported while no entry is installable")
			}
			for _, m := range r.Models {
				if m.Installable || !strings.HasPrefix(m.InstallBlocked, "not pinned: missing revision") {
					t.Errorf("%s claims to be installable: %+v", m.ID, m)
				}
				if m.Verdict == VerdictRunsNow && r.Recommendation.RunnableNow == false && r.Recommendation.ModelID == "" {
					t.Errorf("runs_now model exists but nothing recommended")
				}
			}
			if r.Recommendation.RunnableNow {
				if model(t, r, r.Recommendation.ModelID).Verdict != VerdictRunsNow {
					t.Error("RunnableNow recommendation is not a runs_now model")
				}
			}
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), ":null") {
				t.Errorf("report contains JSON null (arrays must be empty, not null): %s", b)
			}
		})
	}
}

func TestOwnerReserveReducesHeadroom(t *testing.T) {
	// 12 GiB free, large cap. Qwen3-8B q4 needs about 6.8 GiB.
	low := run(t, fakeSource{hosts: []Host{selfMac(16, 12, 32, 1)}}, healthy)
	high := run(t, fakeSource{hosts: []Host{selfMac(16, 12, 32, 8)}}, healthy)
	if low.Machines[0].HeadroomBytes != 11*gb || high.Machines[0].HeadroomBytes != 4*gb {
		t.Fatalf("headroom low=%d high=%d", low.Machines[0].HeadroomBytes, high.Machines[0].HeadroomBytes)
	}
	if low.Machines[0].UsableBytes != 11*gb || high.Machines[0].UsableBytes != 4*gb {
		t.Errorf("usable low=%d high=%d", low.Machines[0].UsableBytes, high.Machines[0].UsableBytes)
	}
	if got := model(t, low, "qwen3-8b-q4").Verdict; got != VerdictRunsNow {
		t.Errorf("small reserve: %s", got)
	}
	if got := model(t, high, "qwen3-8b-q4").Verdict; got != VerdictDoesNotFit {
		t.Errorf("large reserve: %s", got)
	}
	if low.Recommendation.ModelID == high.Recommendation.ModelID {
		t.Errorf("the reserve should change the recommendation: %s", low.Recommendation.ModelID)
	}
}

func TestPeerDefaultsAreLabelledAssumptions(t *testing.T) {
	r := run(t, fakeSource{hosts: []Host{selfMac(24, 20, 16, 2), peerMac("a", 32, 28, true)}, links: []Link{goodLink("a")}}, healthy)
	p := r.Machines[1]
	if p.AdmissionSource != "assumed-peer-defaults" || p.ApprovedMemoryBytes != PolicyMemoryLimitMax || p.OwnerReserveBytes != 8*gb {
		t.Errorf("peer = %+v", p)
	}
	if p.Runtime.State != RuntimeUnverified || p.Runtime.Verified {
		t.Errorf("a peer's runtime must be reported unverified: %+v", p.Runtime)
	}
	joined := strings.Join(r.Assumptions, " ")
	if !strings.Contains(joined, "not visible remotely") {
		t.Errorf("assumptions = %v", r.Assumptions)
	}
	if r.Links[0].BandwidthMbps == nil || *r.Links[0].BandwidthMbps != 900 {
		t.Errorf("measured bandwidth lost: %+v", r.Links[0])
	}
}

func TestUnmeasuredLinkValuesStayAbsent(t *testing.T) {
	l := Link{PeerID: "a", Lifecycle: "connected", Path: "direct", PQ: "protected"}
	r := run(t, fakeSource{hosts: []Host{selfMac(24, 20, 16, 2), peerMac("a", 24, 20, true)}, links: []Link{l}}, healthy)
	got := r.Links[0]
	if got.BandwidthMbps != nil || got.LatencyMS != nil || got.PacketLossPercent != nil || got.Quality != "fair" {
		t.Errorf("link = %+v", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "bandwidthMbps") {
		t.Errorf("unmeasured bandwidth must be omitted: %s", b)
	}
	found := false
	for _, g := range r.Gaps {
		if strings.Contains(g, "Bandwidth to") {
			found = true
		}
	}
	if !found {
		t.Errorf("gaps = %v", r.Gaps)
	}
}

func TestDiskLowBlocksSingleHost(t *testing.T) {
	h := selfMac(64, 50, 48, 4)
	h.DiskFreeBytes = 12 * gb // 10 GiB floor leaves 2 GiB
	r := run(t, fakeSource{hosts: []Host{h}}, healthy)
	if got := model(t, r, "qwen3-32b-q4").Verdict; got != VerdictFitsSingleBlocked {
		t.Errorf("32b-q4 = %s", got)
	}
	if !hasBlocker(r, "disk-low") {
		t.Error("expected disk-low")
	}
	if got := model(t, r, "qwen3-0.6b-q4").Verdict; got != VerdictRunsNow {
		t.Errorf("0.6b-q4 weights fit in 2 GiB, got %s", got)
	}
}

func TestHostSourceErrorBecomesGap(t *testing.T) {
	r := run(t, fakeSource{herr: context.DeadlineExceeded, extras: StatusExtras{Reachable: true}, exact: true}, healthy)
	if len(r.Gaps) == 0 || !hasBlocker(r, "connector-unreachable") || len(r.Machines) != 0 {
		t.Errorf("gaps=%v blockers=%v", r.Gaps, r.Blockers)
	}
}

func TestSharingStatementsAreHonest(t *testing.T) {
	s := buildSharing(true, true, "disabled")
	if s.MultiHostExecution != "unavailable" || !strings.Contains(s.Statement, "multi-host execution is not available yet") {
		t.Errorf("%+v", s)
	}
	joined := strings.Join(s.Evidence, "\n")
	for _, want := range []string{"executionValidated=false", "not distributed inference", "not integrated yet", "distributed_execution=\"disabled\""} {
		if !strings.Contains(joined, want) {
			t.Errorf("evidence lacks %q", want)
		}
	}
	if buildSharing(false, false, "").SingleHostExecution != "blocked" {
		t.Error("single-host must be blocked when no runtime is healthy")
	}
}

func TestHowToUseNamesOnlyRealThings(t *testing.T) {
	cat, _ := LoadCatalog()
	text := strings.Join(howToUse(cat.Entries[0], "MacBook", true), "\n")
	for _, want := range []string{"no web page", "nexal-mlx-job", "not a `nexal` subcommand", "probe", "512"} {
		if !strings.Contains(text, want) {
			t.Errorf("how-to lacks %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{"nexal infer", "http", "localhost"} {
		if strings.Contains(text, bad) {
			t.Errorf("how-to invents %q", bad)
		}
	}
	if strings.Contains(strings.Join(howToUse(cat.Entries[0], "MacBook", false), "\n"), "not runnable yet") == false {
		t.Error("non-runnable how-to must say so")
	}
}

func TestLinkQuality(t *testing.T) {
	online := Host{ID: "p", Name: "P", Online: true}
	tests := []struct {
		name string
		l    Link
		want string
	}{
		{"good", Link{Lifecycle: "connected", Path: "direct", PQ: "protected", LatencyKnown: true, LatencyMS: 3, BandwidthKnown: true, BandwidthMbps: 800}, "good"},
		{"slow", Link{Lifecycle: "connected", Path: "direct", LatencyKnown: true, LatencyMS: 3, BandwidthKnown: true, BandwidthMbps: 40}, "fair"},
		{"far", Link{Lifecycle: "connected", Path: "direct", LatencyKnown: true, LatencyMS: 80, BandwidthKnown: true, BandwidthMbps: 800}, "fair"},
		{"relay", Link{Lifecycle: "connected", Path: "relay", LatencyKnown: true, LatencyMS: 3, BandwidthKnown: true, BandwidthMbps: 800}, "poor"},
		{"flappy", Link{Lifecycle: "connected", Path: "direct", PathFlapsLastHour: 6, LatencyKnown: true, LatencyMS: 3, BandwidthKnown: true, BandwidthMbps: 800}, "poor"},
		{"lossy", Link{Lifecycle: "degraded", Path: "direct", LossKnown: true, PacketLossPercent: 4, LatencyKnown: true, LatencyMS: 3, BandwidthKnown: true, BandwidthMbps: 800}, "poor"},
		{"down", Link{Lifecycle: "authenticating", Path: "direct"}, "unusable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildLinkReport(online, &tc.l).Quality; got != tc.want {
				t.Errorf("quality = %s, want %s", got, tc.want)
			}
		})
	}
	if got := buildLinkReport(online, nil).Quality; got != "poor" {
		t.Errorf("missing link on an online peer = %s", got)
	}
}

func TestReportJSONSchemaIsStable(t *testing.T) {
	r := run(t, fakeSource{hosts: []Host{selfMac(24, 20, 16, 2), peerMac("a", 24, 20, true)}, links: []Link{goodLink("a")}}, healthy)
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schemaVersion", "generatedAt", "headline", "machines", "links", "models", "recommendation", "sharing", "blockers", "gaps", "assumptions", "catalogNote", "surface"} {
		if _, ok := top[k]; !ok {
			t.Errorf("report lacks %q", k)
		}
	}
	var back Report
	if err := json.Unmarshal(b, &back); err != nil || back.SchemaVersion != 1 || len(back.Models) != len(r.Models) {
		t.Errorf("round trip failed: %v", err)
	}
}

func TestRenderTextCoversTheReport(t *testing.T) {
	r := run(t, fakeSource{hosts: []Host{selfMac(24, 20, 16, 2), peerMac("a", 24, 20, true)}, links: []Link{goodLink("a")}}, healthy)
	txt := RenderText(r)
	for _, want := range []string{r.Headline, "MacBook (this Mac)", "Mini-a", "900 Mbps", "Sharing across peers", "Recommended:", "Fix:", "nexal-mlx-job", "What this check could not see"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q", want)
		}
	}
}

func TestReportStaysUnderTheAppsOutputCap(t *testing.T) {
	// The Mac app captures at most 65,536 bytes of stdout per command.
	hosts := []Host{selfMac(128, 100, 96, 8)}
	var links []Link
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		hosts = append(hosts, peerMac(id, 64, 50, true))
		links = append(links, goodLink(id))
	}
	b, _ := json.Marshal(run(t, fakeSource{hosts: hosts, links: links}, healthy))
	if len(b) > 40*1024 {
		t.Errorf("report is %d bytes with 8 peers; the app's capture cap is 65536", len(b))
	}
}

func TestDownloadBytesInVerdict(t *testing.T) {
	r := run(t, fakeSource{hosts: []Host{selfMac(36, 30, 8, 2)}}, healthy)
	for _, m := range r.Models {
		if m.DownloadBytes != 0 {
			t.Errorf("%s unpinned but downloadBytes=%d", m.ID, m.DownloadBytes)
		}
	}
	b, _ := json.Marshal(r.Models[0])
	if strings.Contains(string(b), "downloadBytes") {
		t.Error("downloadBytes must be omitted when unpinned")
	}
	f := newFakeHF(t)
	cat := f.pinnedCatalog(t)
	var want int64
	for _, pf := range f.pinFiles() {
		want += pf.Size
	}
	src := fakeSource{hosts: []Host{selfMac(36, 30, 8, 2)}, extras: StatusExtras{Reachable: true, MeshPQ: "protected"}}
	rep, err := Engine{Hosts: src, Links: src, Runtime: fakeRuntime{healthy}, Catalog: cat}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range rep.Models {
		if m.ID == testID {
			if m.DownloadBytes != want || !m.Installable {
				t.Errorf("downloadBytes=%d want %d", m.DownloadBytes, want)
			}
			b, _ := json.Marshal(m)
			if !strings.Contains(string(b), `"downloadBytes":`) {
				t.Error("field missing from JSON")
			}
		} else if m.DownloadBytes != 0 {
			t.Errorf("%s leaked downloadBytes", m.ID)
		}
	}
}
