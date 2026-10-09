package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"nexal/connector/internal/pool"
)

// Assumptions applied to Macs whose owner policy cannot be read remotely. They
// are reported verbatim in Report.Assumptions.
const (
	// PolicyMemoryLimitMax mirrors config.ResourcePolicy.Validate, which rejects a
	// workload memory cap above 8 GiB. It is the largest cap ANY Mac can hold, so
	// it is the honest upper bound for a peer whose cap is unknown.
	PolicyMemoryLimitMax = uint64(8) << 30
	// assumedPeerReserveFraction: share of a peer's RAM treated as its owner
	// reserve, because the real value is not visible remotely.
	assumedPeerReserveFraction = 0.25
	// defaultMinFreeDiskBytes mirrors the connector's 10 GiB disk reserve default
	// (config.ResourcePolicy MinFreeDiskBytes == 0).
	defaultMinFreeDiskBytes = uint64(10) << 30
	// maxShardRanks bounds the layout search.
	maxShardRanks = 8
)

type node struct {
	host     Host
	link     *Link
	approved uint64
	reserve  uint64
	headroom uint64
	usable   uint64
	source   string
	runtime  RuntimeReport
	eligible bool
	reasons  []string
	peer     pool.MLXPeer
	hypoPeer pool.MLXPeer // same Mac with the owner cap ignored (what-if only)
	quality  string
}

func deviceID(id string) string {
	sum := sha256.Sum256([]byte("nexal-preflight-device:" + id))
	return hex.EncodeToString(sum[:])
}

// planningDigest is a PLANNING TOKEN so pool.PlanMLX (which demands a 64-hex
// digest) can run before any real model digest exists. It is derived from the
// catalog id; it is not a hash of any model file and is never shown as one.
func planningDigest(id string) string {
	sum := sha256.Sum256([]byte("nexal-preflight-planning-token:" + id))
	return hex.EncodeToString(sum[:])
}

func parseMacOS(os string) (int, int) {
	var major, minor int
	i := strings.Index(os, "macOS ")
	if i < 0 {
		return 0, 0
	}
	fmt.Sscanf(os[i+len("macOS "):], "%d.%d", &major, &minor)
	return major, minor
}

func gib(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

func (e Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Engine) buildNodes(hosts []Host, links []Link, probe RuntimeProbe, pins [2]string) []*node {
	byID := map[string]*Link{}
	for i := range links {
		byID[links[i].PeerID] = &links[i]
	}
	var nodes []*node
	for _, h := range hosts {
		n := &node{host: h}
		if !h.IsSelf {
			n.link = byID[h.ID]
		}
		total := h.TotalMemoryBytes
		switch {
		case h.IsSelf && h.AdmissionKnown:
			n.approved, n.reserve, n.source = h.ApprovedMemoryBytes, h.OwnerReserveBytes, "local-policy"
		case h.IsSelf:
			n.source = "unavailable"
		default:
			n.approved = PolicyMemoryLimitMax
			if total < n.approved {
				n.approved = total
			}
			n.reserve = uint64(float64(total) * assumedPeerReserveFraction)
			n.source = "assumed-peer-defaults"
		}
		if h.MemoryKnown && h.AvailableMemoryBytes > n.reserve {
			n.headroom = h.AvailableMemoryBytes - n.reserve
		}
		n.usable = n.approved
		if n.headroom < n.usable {
			n.usable = n.headroom
		}

		// Eligibility.
		var why []string
		if !h.Online {
			why = append(why, "Offline or asleep.")
		}
		if !h.MemoryKnown {
			why = append(why, "Memory figures were not reported.")
		}
		if h.Chip == "" || !strings.HasPrefix(strings.ToLower(h.Chip), "apple") {
			why = append(why, "Not reported as an Apple Silicon Mac (MLX needs Apple Silicon).")
		}
		if h.IsSelf && !h.AdmissionKnown {
			why = append(why, "This Mac's memory policy could not be read from the connector.")
		}
		if h.OffersCompute != nil && !*h.OffersCompute {
			why = append(why, "This Mac is not offering compute.")
		}
		if !h.IsSelf {
			switch {
			case n.link == nil:
				why = append(why, "No mesh link information.")
			case n.link.PQ != "protected":
				r := "post-quantum protection is not verified"
				if n.link.PQReason != "" {
					r += " (" + n.link.PQReason + ")"
				}
				why = append(why, "Link not trusted: "+r+".")
			}
		}
		if h.MemoryKnown && n.usable == 0 && len(why) == 0 {
			why = append(why, "No usable memory after the owner reserve and cap.")
		}
		n.reasons, n.eligible = why, len(why) == 0

		// Runtime.
		if h.IsSelf {
			n.runtime = RuntimeReport{State: probe.State, Detail: probe.Detail, Version: probe.RuntimeVersion,
				MLXVersion: probe.MLXVersion, MLXLMVersion: probe.MLXLMVersion,
				DistributedExecution: probe.DistributedExecution, Verified: true}
			if probe.State == "" {
				n.runtime.State, n.runtime.Detail = RuntimeProbeFailed, "The runtime probe returned no state."
			}
		} else {
			n.runtime = RuntimeReport{State: RuntimeUnverified, Detail: "The MLX runtime on another Mac cannot be probed from here. Run the preflight on that Mac."}
		}

		// Planner inputs. Versions are the runtime's pins: whether a Mac really
		// has them is the runtime state above, not something the planner checks.
		mk := func(headroom uint64, approved uint64) pool.MLXPeer {
			maj, min := h.MacOSMajor, h.MacOSMinor
			if maj == 0 {
				maj, min = parseMacOS(h.OS)
			}
			return pool.MLXPeer{DeviceID: deviceID(h.ID), Trusted: true, Available: true, Chip: h.Chip,
				MacOSMajor: maj, MacOSMinor: min, ApprovedMemory: int64(approved), MeasuredHeadroom: int64(headroom),
				MLXVersion: pins[0], MLXLMVersion: pins[1], Backends: []pool.MLXBackend{pool.TCPRing}}
		}
		n.peer = mk(n.headroom, n.approved)
		n.hypoPeer = mk(n.headroom, n.headroom)

		if h.IsSelf {
			n.quality = "good"
		} else {
			n.quality = buildLinkReport(h, n.link).Quality
		}
		nodes = append(nodes, n)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if a.host.IsSelf != b.host.IsSelf {
			return a.host.IsSelf
		}
		return a.host.Name < b.host.Name
	})
	return nodes
}

func (e Engine) plan(m CatalogEntry, ranks []pool.RankMemory, peers []pool.MLXPeer) (pool.PlacementPlan, error) {
	return pool.PlanMLX(pool.PlacementRequest{
		ModelDigest: planningDigest(m.ID), Architecture: m.Architecture, Quantization: m.Quantization,
		PartitionStrategy: "even-split (planning only)", MLXVersion: e.Catalog.MLXVersion, MLXLMVersion: e.Catalog.MLXLMVersion,
		ValidatedModel: true, // what-if: no model is installed, so no registry evidence exists
		Backend:        pool.TCPRing, Ranks: ranks, Peers: peers,
	})
}

func diskCheck(n *node, need int64) (bool, string) {
	if !n.host.DiskKnown {
		return true, ""
	}
	floor := n.host.DiskReserveBytes
	if floor == 0 {
		floor = defaultMinFreeDiskBytes
	}
	if n.host.DiskFreeBytes < floor || int64(n.host.DiskFreeBytes-floor) < need {
		return false, fmt.Sprintf("%s has %s free disk; the model files need about %s plus the owner's %s free-space floor.",
			n.host.Name, gib(int64(n.host.DiskFreeBytes)), gib(need), gib(int64(floor)))
	}
	return true, ""
}

// Run executes the preflight. A source error does not abort: it becomes a gap
// and a blocker, because a half-answer with its holes labelled beats no answer.
func (e Engine) Run(ctx context.Context) (Report, error) {
	rep := Report{SchemaVersion: SchemaVersion, GeneratedAt: e.now().UTC().Format(time.RFC3339),
		Machines: []MachineReport{}, Links: []LinkReport{}, Models: []ModelVerdict{}, Blockers: []Blocker{},
		Gaps: []string{}, Assumptions: []string{}, CatalogNote: e.Catalog.EstimateLabel,
		Surface: "There is no web UI for inference. The only surface today is the command line on the Mac that holds the model; this check is read-only and installs nothing."}
	var blockers []Blocker
	add := func(b Blocker) {
		for _, x := range blockers {
			if x.Code == b.Code && x.Scope == b.Scope {
				return
			}
		}
		blockers = append(blockers, b)
	}

	hosts, herr := e.Hosts.Hosts(ctx)
	var links []Link
	var lerr error
	if e.Links != nil {
		links, lerr = e.Links.Links(ctx)
	}
	extras := StatusExtras{Reachable: true}
	if x, ok := e.Hosts.(ExtrasSource); ok {
		extras = x.Extras()
	}
	probe := RuntimeProbe{State: RuntimeProbeFailed, Detail: "No runtime prober configured."}
	if e.Runtime != nil {
		probe = e.Runtime.Probe(ctx)
	}
	if herr != nil {
		rep.Gaps = append(rep.Gaps, "Host facts could not be read: "+herr.Error())
	}
	if lerr != nil {
		rep.Gaps = append(rep.Gaps, "Link facts could not be read: "+lerr.Error())
	}
	if !extras.Reachable || herr != nil {
		add(Blocker{Code: "connector-unreachable", Severity: SeverityBlocker, Scope: "this-mac",
			Title:  "The neXal connector is not answering",
			Detail: "Memory policy, peers and links come from the running connector, so without it this check cannot say what fits.",
			Fix:    "Open the neXal app and make sure neXal@home is running, then run the check again."})
	}

	nodes := e.buildNodes(hosts, links, probe, [2]string{e.Catalog.MLXVersion, e.Catalog.MLXLMVersion})
	var self *node
	for _, n := range nodes {
		if n.host.IsSelf {
			self = n
		}
	}
	if self == nil {
		rep.Gaps = append(rep.Gaps, "This Mac was not reported by the connector.")
	}

	// Machines and links.
	var peersSeen int
	for _, n := range nodes {
		m := MachineReport{ID: n.host.ID, Name: n.host.Name, IsSelf: n.host.IsSelf, Online: n.host.Online,
			Chip: n.host.Chip, OS: n.host.OS, TotalMemoryBytes: n.host.TotalMemoryBytes,
			AvailableMemoryBytes: n.host.AvailableMemoryBytes, ApprovedMemoryBytes: n.approved,
			OwnerReserveBytes: n.reserve, HeadroomBytes: n.headroom, UsableBytes: n.usable, AdmissionSource: n.source,
			DiskFreeBytes: n.host.DiskFreeBytes, DiskKnown: n.host.DiskKnown, DiskReserveByte: n.host.DiskReserveBytes,
			Runtime: n.runtime, Eligible: n.eligible, Reasons: append([]string{}, n.reasons...)}
		if m.Reasons == nil {
			m.Reasons = []string{}
		}
		rep.Machines = append(rep.Machines, m)
		if n.host.IsSelf {
			continue
		}
		peersSeen++
		lr := buildLinkReport(n.host, n.link)
		rep.Links = append(rep.Links, lr)
		if !n.host.Online {
			add(Blocker{Code: "peer-offline", Severity: SeverityWarning, Scope: n.host.Name,
				Title: n.host.Name + " is offline", Detail: "An offline Mac cannot contribute memory or run a model.",
				Fix: "Wake it or bring it back online, then run the check again."})
		}
		if lr.Unstable {
			add(Blocker{Code: "link-unstable", Severity: SeverityWarning, Scope: n.host.Name,
				Title:  "The link to " + n.host.Name + " keeps switching paths",
				Detail: fmt.Sprintf("It changed between direct and relay %d times in the last hour.", lr.PathFlapsLastHour),
				Fix:    "Put both Macs on the same network, or cable them together, so the direct path stays up."})
		} else if lr.Online && lr.Path != "direct" && lr.Quality != "unusable" {
			add(Blocker{Code: "link-relayed", Severity: SeverityWarning, Scope: n.host.Name,
				Title:  "Traffic to " + n.host.Name + " goes through a relay",
				Detail: "A relayed path is slower and less steady than a direct one, which matters if a model is ever split across Macs.",
				Fix:    "Put both Macs on the same network so the mesh can use a direct path."})
		}
		if n.link != nil && n.link.PQ != "protected" && n.host.Online {
			add(Blocker{Code: "peer-pq-unverified", Severity: SeverityWarning, Scope: n.host.Name,
				Title:  "The link to " + n.host.Name + " is not post-quantum verified",
				Detail: "The connector refuses to share compute over a link that is not verified, so this Mac is left out.",
				Fix:    "Wait for the key exchange to finish, or update neXal on that Mac."})
		}
	}
	for _, l := range rep.Links {
		if l.Online && l.BandwidthMbps == nil {
			rep.Gaps = append(rep.Gaps, "Bandwidth to "+l.PeerName+" has not been measured yet.")
		}
	}

	// Runtime blockers (this Mac only).
	if self != nil {
		if rb := runtimeBlocker(self.runtime); rb != nil {
			add(*rb)
		}
		if self.host.DiskKnown == false {
			rep.Gaps = append(rep.Gaps, "Free disk space on this Mac could not be read.")
		}
	}
	if extras.Reachable && extras.MeshPQ != "" && extras.MeshPQ != "protected" {
		add(Blocker{Code: "pq-not-ready", Severity: SeverityWarning, Scope: "this-mac",
			Title:  "Post-quantum protection is not fully verified",
			Detail: "The connector's admission check requires quantum-safe peer verification before it will share compute (status: " + extras.MeshPQ + ").",
			Fix:    "Wait for the secure-network key exchange to complete; it is shown on the neXal panel."})
	}
	if extras.ExecutionBlocker != "" {
		add(Blocker{Code: "connector-execution-gated", Severity: SeverityInfo, Scope: "this-mac",
			Title:  "The connector's job admission is currently closed",
			Detail: "Its own reason: " + extras.ExecutionBlocker + ". This gates connector-dispatched jobs; the owner-run nexal-mlx-job path does not use it.",
			Fix:    "No action needed for owner-run single-model inference."})
	}

	// Assumptions and gaps that are always true.
	if peersSeen > 0 {
		rep.Assumptions = append(rep.Assumptions,
			fmt.Sprintf("Another Mac's owner memory cap and reserve are not visible remotely. Each is assumed to have a cap of %d MiB (the largest the connector accepts) and a reserve of %d%% of its RAM.", PolicyMemoryLimitMax>>20, int(assumedPeerReserveFraction*100)))
		rep.Gaps = append(rep.Gaps,
			"The MLX runtime on other Macs cannot be probed remotely; run this check on each Mac.",
			"Only links from this Mac are measured; links between two peers are not visible.",
			"Whether a peer accepts compute (paused, policy) is not reported.")
	}
	rep.Assumptions = append(rep.Assumptions,
		"Model sizes are estimates from parameter count and bits per weight, not measurements.",
		"Layouts are checked with pool.PlanMLX using a planning token in place of a model digest, because no model is pinned or installed.",
		"Model architecture figures in the catalog were not read from a pinned config.json.")
	rep.Gaps = append(rep.Gaps,
		"RDMA and Thunderbolt capability is not reported by any layer (unknown, not absent).",
		"Whether model weights are already on disk is not checked.")

	// Models.
	var eligible []*node
	for _, n := range nodes {
		if n.eligible {
			eligible = append(eligible, n)
		}
	}
	// Single-host search order: this Mac first, then by usable memory.
	single := append([]*node(nil), eligible...)
	sort.SliceStable(single, func(i, j int) bool {
		if single[i].host.IsSelf != single[j].host.IsSelf {
			return single[i].host.IsSelf
		}
		return single[i].usable > single[j].usable
	})
	selfHealthy := self != nil && self.runtime.State == RuntimeHealthy
	anySharded := false
	capLimited := ""
	exceedsMax := false

	for _, m := range e.Catalog.Entries {
		v := ModelVerdict{ID: m.ID, DisplayName: m.DisplayName, ParamsBillions: m.ParamsBillions, Quantization: m.Quantization,
			Reasons: []string{}, Installable: m.Installable, InstallBlocked: m.InstallBlockedReason,
			ModelProvisioning: "Model files are not detected by this check; the owner must provision a reviewed copy."}
		need, _ := m.Estimate.Total()
		for _, f := range m.Files {
			v.DownloadBytes += f.Size
		}
		v.RequiredBytesSingleHost = need
		weights := m.Estimate.WeightsBytes

		var fit *node
		var diskNote string
		var diskNode *node
		for _, n := range single {
			if _, err := e.plan(m, []pool.RankMemory{m.Estimate}, []pool.MLXPeer{n.peer}); err != nil {
				continue
			}
			if ok, why := diskCheck(n, weights); !ok {
				if diskNode == nil {
					diskNode, diskNote = n, why
				}
				continue
			}
			fit = n
			break
		}
		switch {
		case fit != nil && fit.host.IsSelf && selfHealthy:
			v.Verdict, v.HostID, v.HostName = VerdictRunsNow, fit.host.ID, fit.host.Name
			v.Summary = fmt.Sprintf("Runs now on %s (about %s needed).", fit.host.Name, gib(need))
		case fit != nil:
			v.Verdict, v.HostID, v.HostName = VerdictFitsSingleBlocked, fit.host.ID, fit.host.Name
			if fit.host.IsSelf {
				v.Summary = fmt.Sprintf("Fits on %s, but its MLX runtime is not ready (%s).", fit.host.Name, strings.ReplaceAll(fit.runtime.State, "_", " "))
				v.Reasons = append(v.Reasons, fit.runtime.Detail)
			} else {
				v.Summary = fmt.Sprintf("Fits on %s, but this check cannot confirm its runtime from here.", fit.host.Name)
				v.Reasons = append(v.Reasons, "Run the preflight on "+fit.host.Name+" to check its MLX runtime.")
			}
		case diskNode != nil:
			v.Verdict, v.HostID, v.HostName = VerdictFitsSingleBlocked, diskNode.host.ID, diskNode.host.Name
			v.Summary = fmt.Sprintf("Fits in memory on %s, but there is not enough free disk.", diskNode.host.Name)
			v.Reasons = append(v.Reasons, diskNote)
			add(Blocker{Code: "disk-low", Severity: SeverityBlocker, Scope: diskNode.host.Name,
				Title: "Not enough free disk space on " + diskNode.host.Name, Detail: diskNote,
				Fix: "Free disk space on that Mac (or choose a smaller model)."})
		default:
			e.classifyNoSingle(&v, m, eligible, nodes, &anySharded, &capLimited, &exceedsMax)
		}
		if !m.Installable {
			v.Reasons = append(v.Reasons, "Cannot be installed yet: "+m.InstallBlockedReason+".")
		}
		rep.Models = append(rep.Models, v)
	}

	// Aggregate blockers that come from the model results.
	if capLimited != "" {
		add(Blocker{Code: "owner-memory-cap", Severity: SeverityBlocker, Scope: "this-mac",
			Title:      "The owner memory cap is the limit, not the hardware",
			Detail:     capLimited,
			Fix:        "Raise the cap with `nexal set-policy`; all three flags are required, so pass your current reserve and idle values too.",
			FixCommand: "nexal set-policy --memory-limit-mib 8192 --reserve-memory-mib <RESERVE_MIB> --idle-seconds <IDLE_SECONDS>"})
	}
	if exceedsMax {
		add(Blocker{Code: "policy-cap-maximum", Severity: SeverityWarning, Scope: "all",
			Title:  "The connector cannot approve more than 8 GiB per Mac",
			Detail: "config.ResourcePolicy rejects a workload memory cap above 8 GiB, so a model needing more than that on one Mac cannot be admitted, whatever the Mac's RAM.",
			Fix:    "This is a code limit, not a setting: it needs a reviewed change to the policy bounds."})
	}
	if anySharded {
		add(Blocker{Code: "multi-host-unavailable", Severity: SeverityInfo, Scope: "all",
			Title:  "Splitting a model across Macs is not built yet",
			Detail: "A layout exists on paper (pool.PlanMLX), but nothing can execute it: the only multi-host code is a scalar all-sum test and an all-reduce primitive.",
			Fix:    "Nothing to do today. Run a smaller model on one Mac, or use a Mac with more memory."})
	}
	if peersSeen == 0 {
		add(Blocker{Code: "no-peers", Severity: SeverityInfo, Scope: "all",
			Title:  "No other Mac is available to share with",
			Detail: "Only this Mac can host a model.", Fix: "Pair another Mac to your neXal network if you want to plan for sharing later."})
	}
	anyInstallable := false
	for _, m := range e.Catalog.Entries {
		anyInstallable = anyInstallable || m.Installable
	}
	if !anyInstallable {
		add(Blocker{Code: "catalog-not-pinned", Severity: SeverityInfo, Scope: "catalog",
			Title:      "No catalog model can be installed yet",
			Detail:     "No catalog entry is pinned: revision and per-file SHA-256 are missing, and the runtime refuses anything it has not pinned.",
			Fix:        "On a Mac with network access run `nexal inference pin <model-id> --revision <40-hex-commit>` for the model you want, review the result, then `nexal inference install <model-id>`. Or provision a checkpoint by hand as runtimes/README.md describes.",
			FixCommand: "nexal inference pin <model-id> --revision <40-hex-commit>"})
	}

	rep.Recommendation = e.recommend(rep.Models, nodes)
	rep.Sharing = buildSharing(anySharded, selfHealthy, probe.DistributedExecution)
	rep.Blockers = sortBlockers(blockers)
	rep.Headline = headline(rep)
	return rep, nil
}

// classifyNoSingle handles a model that fits on no single eligible host.
func (e Engine) classifyNoSingle(v *ModelVerdict, m CatalogEntry, eligible, all []*node, anySharded *bool, capLimited *string, exceedsMax *bool) {
	need := v.RequiredBytesSingleHost
	// What-if: owner cap ignored.
	var hypo *node
	for _, n := range eligible {
		if n.approved < n.headroom {
			if _, err := e.plan(m, []pool.RankMemory{m.Estimate}, []pool.MLXPeer{n.hypoPeer}); err == nil {
				if hypo == nil || n.host.IsSelf {
					hypo = n
				}
			}
		}
	}
	if hypo != nil {
		if uint64(need) <= PolicyMemoryLimitMax {
			v.FitsIfCapRaised = true
			if *capLimited == "" {
				*capLimited = fmt.Sprintf("%s has %s of free memory after its reserve, but its approved cap is only %s, which is what keeps larger models from fitting.",
					hypo.host.Name, gib(int64(hypo.headroom)), gib(int64(hypo.approved)))
			}
		} else {
			*exceedsMax = true
			v.Reasons = append(v.Reasons, fmt.Sprintf("Needs about %s on one Mac, but the connector cannot approve more than 8 GiB.", gib(need)))
		}
	}

	// Sharded search.
	if len(eligible) >= 2 {
		kmax := len(eligible)
		if kmax > maxShardRanks {
			kmax = maxShardRanks
		}
		var good, peersAll []pool.MLXPeer
		for _, n := range eligible {
			peersAll = append(peersAll, n.peer)
			if n.quality == "good" || n.quality == "fair" {
				good = append(good, n.peer)
			}
		}
		try := func(peers []pool.MLXPeer) (pool.PlacementPlan, bool) {
			for k := 2; k <= kmax && k <= len(peers); k++ {
				ranks := make([]pool.RankMemory, k)
				for i := range ranks {
					ranks[i] = EstimateRank(m.Arch, m.ParamsBillions, m.BitsPerWeight, k)
				}
				if plan, err := e.plan(m, ranks, peers); err == nil {
					return plan, true
				}
			}
			return pool.PlacementPlan{}, false
		}
		plan, ok := try(good)
		if !ok {
			plan, ok = try(peersAll)
		}
		if ok {
			byDev := map[string]*node{}
			for _, n := range eligible {
				byDev[n.peer.DeviceID] = n
			}
			linksOK := true
			for _, r := range plan.Ranks {
				n := byDev[r.DeviceID]
				v.Layout = append(v.Layout, RankLayout{Rank: r.Rank, MachineID: n.host.ID, MachineName: n.host.Name,
					RequiredBytes: r.RequiredBytes, UsableBytes: r.UsableBytes})
				if !n.host.IsSelf && n.quality != "good" && n.quality != "fair" {
					linksOK = false
				}
			}
			v.LayoutLinksOK = &linksOK
			v.Verdict = VerdictFitsShardedOnly
			*anySharded = true
			v.Summary = fmt.Sprintf("Fits only when split across %d Macs. Not runnable yet: multi-host execution is not available.", len(plan.Ranks))
			if !linksOK {
				v.Reasons = append(v.Reasons, "At least one Mac in the layout has a relayed, unstable or down link, so sharing would be unreliable even once it exists.")
			}
			return
		}
	}
	v.Verdict = VerdictDoesNotFit
	var best int64
	var bestName string
	for _, n := range eligible {
		if int64(n.usable) > best {
			best, bestName = int64(n.usable), n.host.Name
		}
	}
	switch {
	case len(eligible) == 0:
		v.Summary = "Does not fit: no Mac is eligible."
		v.Reasons = append(v.Reasons, "No Mac passed the eligibility checks; see the machine list.")
	case len(eligible) == 1:
		v.Summary = fmt.Sprintf("Does not fit: needs about %s; %s can offer %s and there is no other Mac to share with.", gib(need), bestName, gib(best))
	default:
		var total int64
		for _, n := range eligible {
			total += int64(n.usable)
		}
		v.Summary = fmt.Sprintf("Does not fit: needs about %s on one Mac (the most any one Mac can offer is %s), and no layout across %d Macs fits their per-Mac headroom.", gib(need), gib(best), len(eligible))
		v.Reasons = append(v.Reasons, fmt.Sprintf("Combined usable memory is %s, but each Mac's share must cover its own weights, KV cache, activations and safety margin.", gib(total)))
	}
	if v.FitsIfCapRaised {
		v.Summary += " It would fit if the owner memory cap were raised."
	}
}

func (e Engine) recommend(models []ModelVerdict, nodes []*node) Recommendation {
	var entry = map[string]CatalogEntry{}
	for _, c := range e.Catalog.Entries {
		entry[c.ID] = c
	}
	pick := func(want Verdict) *ModelVerdict {
		for i := range models {
			if models[i].Verdict == want {
				return &models[i]
			}
		}
		return nil
	}
	if v := pick(VerdictRunsNow); v != nil {
		return Recommendation{ModelID: v.ID, DisplayName: v.DisplayName, RunnableNow: true, HostName: v.HostName,
			Reason:   fmt.Sprintf("The largest catalog model that fits %s and has a healthy runtime.", v.HostName),
			HowToUse: howToUse(entry[v.ID], v.HostName, true)}
	}
	if v := pick(VerdictFitsSingleBlocked); v != nil {
		return Recommendation{ModelID: v.ID, DisplayName: v.DisplayName, RunnableNow: false, HostName: v.HostName,
			Reason:   "The largest catalog model that fits one Mac's memory. It cannot run until the blockers below are fixed.",
			HowToUse: howToUse(entry[v.ID], v.HostName, false)}
	}
	reason := "No catalog model fits on any single Mac."
	if pick(VerdictFitsShardedOnly) != nil {
		reason = "Larger models only fit when split across Macs, which cannot run yet. No catalog model fits a single Mac."
	}
	return Recommendation{Reason: reason, HowToUse: []string{
		"There is no web page or app screen for running a model today.",
		"Free memory or raise the owner memory cap (see the blockers), then run this check again."}}
}

func headline(r Report) string {
	rec := r.Recommendation
	switch {
	case rec.ModelID != "" && rec.RunnableNow:
		return fmt.Sprintf("Best model you can run now: %s on %s.", rec.DisplayName, rec.HostName)
	case rec.ModelID != "":
		return fmt.Sprintf("Your hardware can hold %s on %s, but it cannot run until the items below are fixed.", rec.DisplayName, rec.HostName)
	default:
		return "No catalog model fits your hardware as configured."
	}
}

func runtimeBlocker(rt RuntimeReport) *Blocker {
	b := Blocker{Code: "runtime-" + strings.ReplaceAll(rt.State, "_", "-"), Severity: SeverityBlocker, Scope: "this-mac", Detail: rt.Detail}
	switch rt.State {
	case RuntimeHealthy:
		return nil
	case RuntimeNotConfigured:
		b.Title = "The MLX runtime is not set up on this Mac"
		b.Fix = "Provision the approved runtime as an owner operation (runtimes/README.md, \"Local provisioning\") and write its owner-policy.json beside the connector config, or pass --runtime-owner-policy."
	case RuntimeIntegrityMismatch:
		b.Title = "The MLX runtime files do not match the owner's pins"
		b.Fix = "Restore the reviewed runtime directory and interpreter, or re-review and re-pin them in owner-policy.json."
	case RuntimeUnsupportedPlatform:
		b.Title = "This Mac cannot run the MLX runtime"
		b.Fix = "MLX needs an Apple Silicon Mac running macOS."
	case RuntimeDependenciesMissing:
		b.Title = "The MLX runtime's Python packages are missing"
		b.Fix = "Install the pinned packages (mlx, mlx-lm, transformers) in the approved environment as runtimes/RELEASE-GATES.md describes."
	case RuntimeVersionMismatch:
		b.Title = "The MLX packages are not the pinned versions"
		b.Fix = "Reinstall the exact versions the runtime pins (mlx 0.29.3, mlx-lm 0.28.4, transformers 4.57.6) in the approved environment."
	default:
		b.Title = "The MLX runtime probe did not run"
		b.Fix = "Run `python3 -I /ABSOLUTE/APPROVED/nexal_mlx_entry.py probe` by hand and fix what it reports."
	}
	return &b
}

func sortBlockers(bs []Blocker) []Blocker {
	rank := map[Severity]int{SeverityBlocker: 0, SeverityWarning: 1, SeverityInfo: 2}
	sort.SliceStable(bs, func(i, j int) bool { return rank[bs[i].Severity] < rank[bs[j].Severity] })
	if bs == nil {
		return []Blocker{}
	}
	return bs
}
