# Peer transport and NAT traversal: decision record

Scope: `connector/` only. This records what the peer transport does **today**, what
changed in Phase 1, what is deliberately **not** built, and one conflict with the
platform hardening plan that is raised here and left for the founder to decide.

Nothing in this document or in the Phase 1 code establishes a peer-to-peer
connection over the internet. Read §5 before believing otherwise.

## 0. The plan wins — what it says

`nexal-platform/docs/HARDENING-PLAN.md` §16 is the governing decision. Quoted
verbatim, lines 872–900:

> ### Use go-libp2p for the data plane
>
> It supplies the three pieces this topology needs, in Go:
>
> - **AutoNAT** — determines whether a node is publicly dialable, STUN-like.
> - **Circuit Relay v2** — relayed fallback that can cap resources by connection
>   count, time and bytes. That byte ceiling is the cost control: a fallback path
>   cannot silently run up spend.
> - **DCUtR** — both peers exchange addresses over the relay, measure RTT, then dial
>   simultaneously to punch through.
>
> It also fits the identity model already in the repo. libp2p peer IDs derive from
> public keys, which maps directly onto `DeviceID = sha256(ed25519 pubkey)`
> (`identity.go:33-39`). The ML-DSA migration in §2 has to be planned across both at
> once.
>
> **Alternative considered:** Tailscale embedded via `tsnet` gives excellent NAT
> traversal and WireGuard transport in Go, but its DERP relays are someone else's
> infrastructure and it brings an identity and account model that duplicates yours.
> Prefer libp2p unless you want the operational burden lifted more than you want
> control.
>
> ### Hole punching fails sometimes — decide where relays live
>
> Symmetric NAT and carrier-grade NAT defeat hole punching, so a meaningful minority
> of residential and mobile donors will always need a relay. Run those relays on
> publicly dialable donors, which is what Circuit Relay v2 is designed for, and pay
> them for it. Running relays yourself puts the bandwidth bill back on your balance
> sheet, which is the thing this section exists to avoid.

Two sentences from that passage decide most of what follows. "Running relays
yourself puts the bandwidth bill back on your balance sheet, which is the thing
this section exists to avoid" (line 899) rejects any company-paid relay. "Its DERP
relays are someone else's infrastructure" (line 889) shows the plan already
rejected a managed relay service on exactly the same grounds. Cloudflare TURN is
that same class of thing, so §3 rejects it. The libp2p selection at line 872 is the
conflict raised in §6.

## 1. Today's reality — measured from the code, not from memory

The peer object-transfer path is mutual-TLS HTTPS to a **literal private IP**, and
nothing else.

- `connector/internal/pool/peer.go:334-346` (`ValidPeerEndpoint`, called from
  `NewPeerClient` at `peer.go:466-472`; before Phase 1 this validation was inline
  in `NewPeerClient` around line 444) refuses any endpoint that is not `https`,
  that carries credentials, a query, a fragment, a path, or no explicit port — and
  refuses any host whose parsed IP fails `privateIP`.
- `privateIP` at `peer.go:314-317` is the whole security rule:
  `ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() && (ip.IsPrivate() ||
  ip.IsLoopback() || ip.IsLinkLocalUnicast())`.
- The client transport sets `Proxy: nil` (`peer.go:483`) and dials the literal
  address, so there is **no DNS lookup and no environment proxy**. Redirects are
  refused outright (`peer.go:490`).
- Authorization is not the address. `PeerOptions` (`peer.go:28`) states it:
  "AllowedPeers is an exact device-fingerprint allowlist, not 'trust the LAN'", and
  `PeerClientOptions.ExpectedDeviceID` (`peer.go:450`) pins exactly one peer
  fingerprint per dial. Fingerprints are `DeviceID = sha256(ed25519 pubkey)`
  (`internal/pool/identity.go:33`).

There is **no STUN, no hole punching, no UPnP/NAT-PMP/PCP, and no relay** anywhere
in `connector/`. Discovery is mDNS: `internal/discovery/mdns.go:40-41` joins
`224.0.0.251` and `ff02::fb`, i.e. **link-local multicast**, and
`internal/discovery/peers.go` is explicit that a discovered candidate "is a hint
about where to look. It is never evidence of membership".

## 2. THE KEY FINDING: cross-VLAN transport is already permitted

**Cross-VLAN peer transport is not blocked by the security rule. It is blocked by
discovery only.**

`10.20.0.5` on VLAN 20 satisfies `ip.IsPrivate()` exactly as `192.168.1.5` on
VLAN 1 does — RFC1918 does not care which subnet an address sits in, and
`privateIP` performs no same-subnet test. So `NewPeerClient` will happily dial a
peer through an inter-VLAN router today, unchanged.

What does not cross a VLAN is **mDNS**. 224.0.0.251 is a link-local multicast
group; routers do not forward it. Two Macs on separate VLANs therefore never hear
each other's announcements, and the address list stays empty.

Consequence, and it shrinks this change dramatically: **cross-VLAN needs a way to
SUPPLY an address, not a relaxation of the security rule.** Phase 1 adds static,
owner-configured peer endpoints validated by the *same* rule (§7B). No security
property is loosened; `privateIP` is untouched, and a public address is still
refused everywhere.

## 3. Cloudflare TURN: REJECTED

TURN is a **relay**, not a direct path. Every byte traverses Cloudflare, which
makes it the opposite of what §16 asks for on three independent grounds.

1. **Throughput.** Cloudflare documents that a single TURN client sending or
   receiving above **50–100 Mbps**, or above **5–10 kpps**, may have packets
   dropped deliberately as backpressure
   ([Cloudflare Realtime TURN](https://developers.cloudflare.com/realtime/turn/)).
   The goal here is bulk backup and restore. A path that throttles at 50–100 Mbps
   is the wrong tool for the one workload that matters.
2. **Cost.** TURN egress is **$0.05/GB after a 1,000 GB/month free tier shared
   with the SFU** ([Realtime pricing](https://developers.cloudflare.com/realtime/sfu/pricing/),
   [TURN FAQ](https://developers.cloudflare.com/realtime/turn/faq/)). One 200 GB
   Time Machine restore is a fifth of the free tier; a handful of customers exhaust
   it in a month.
3. **Whose balance sheet — the decisive one.** The bill lands on the company, which
   is precisely what HARDENING-PLAN §16 exists to prevent: "Running relays
   yourself puts the bandwidth bill back on your balance sheet, which is the thing
   this section exists to avoid" (line 899). The plan already rejected Tailscale
   for the same structural reason — "its DERP relays are someone else's
   infrastructure" (line 889). Cloudflare TURN is the same shape with a meter
   attached. §16 also warns that relaying bulk third-party traffic through
   Cloudflare's network risks **account action rather than an overage line**, which
   is a worse failure than a bill.

Also worth recording because it comes up: **a three-way `cloudflared` tunnel is not
possible.** `cloudflared` is an outbound-only connector to the Cloudflare edge. Two
instances cannot peer with each other; each can only reach the edge, so any
"tunnel-to-tunnel" topology is in fact edge-relayed traffic — case 3 again.

When a relay eventually becomes unavoidable (§16: symmetric and carrier-grade NAT
defeat hole punching for a meaningful minority), the plan's answer is Circuit Relay
v2 **on publicly dialable donors, paid for it**, with byte ceilings. Not us, and
not Cloudflare.

## 4. Cloudflare STUN: ACCEPTED

`stun.cloudflare.com:3478` is **free and unlimited**
([TURN FAQ](https://developers.cloudflare.com/realtime/turn/faq/)), and
`turn.cloudflare.com` also answers plain binding requests, which gives a second
distinct server at no cost.

The reason STUN is acceptable while TURN is not is structural, not a matter of
degree: **STUN relays nothing.** A binding request asks "what source address do you
see this packet arriving from", and the answer is our own reflexive address. No
payload crosses Cloudflare, there is no allocation, there is no meter, and there is
no CDN-terms exposure. It is a mirror, not a pipe.

## 5. HONEST SCOPING: STUN alone establishes no connection

This is the part most likely to be over-read, so it is stated flatly.

**Knowing your public address does not make you reachable at it.** A working
internet peer connection needs three things:

1. each side's reflexive address — Phase 1 does this;
2. a **signalling channel** that carries each side's candidates to the other — the
   coordinator does not do this, and no code in `connector/` publishes a reflexive
   address anywhere;
3. a **simultaneous dial** from both sides, timed so each NAT has seen an outbound
   packet before the peer's inbound packet arrives — nothing in `connector/`
   attempts this, and a single-sided dial to a NATed peer is dropped.

Two of the three do not exist. So what Phase 1 actually buys is **observability**:

- am I behind a NAT at all, and what does the internet see me as;
- does my NAT reuse one mapping across destinations (endpoint-independent) or
  allocate per destination (endpoint-dependent/symmetric);
- therefore, **would hole punching even be worth implementing for this host**;
- and, negatively: is outbound UDP 3478 blocked at all, which is itself a useful
  fact about a customer's network.

`nexal doctor --stun` reports exactly that and says so in its own strings. The
classification is a **tri-state including UNKNOWN**, and one sample is always
UNKNOWN — the code refuses to turn a single observation into "hole punching will
work", and a test asserts that no summary string ever contains that claim.

**This does not ship peer-to-peer over the internet.** It ships the measurement
that tells us whether doing so is feasible per host.

## 6. CONFLICT RAISED, NOT RESOLVED: go-libp2p vs the zero-dependency rule

HARDENING-PLAN §16 line 872 selects **go-libp2p** for the data plane (AutoNAT +
DCUtR + Circuit Relay v2). The founder's standing rule for the connector is
**hand-roll it, keep zero dependencies** — currently true: `connector/go.mod`
declares the module and a Go version and nothing else, and the mDNS responder,
token-bucket shaper, ML-KEM bundle relay and the STUN client added here are all
hand-rolled on the standard library.

These directly conflict. The tradeoff, stated honestly in both directions:

**Hand-rolling NAT traversal is a serious undertaking.** It is not one more
protocol decoder. It means ICE candidate gathering and pairing, connectivity checks
with retransmission and pacing, a signalling protocol on the coordinator, DCUtR-style
RTT measurement and synchronised dialling, keepalives to hold mappings open, and a
relay fallback with byte accounting. Each piece is individually bug-prone, most of
the bugs are timing-dependent and only appear on real customer networks, and the
failure mode is "works on my LAN, fails at 30% of homes". The STUN client in this
change is ~600 lines for the *easiest* piece of that stack.

**libp2p is proven but enormous.** It is battle-tested by IPFS on exactly this
problem, it maps onto our identity model (peer IDs derive from public keys, like
`DeviceID = sha256(ed25519 pubkey)`), and it would deliver all three pieces at
once. It is also a very large transitive dependency tree pulled into a binary that
runs on customers' Macs — a supply-chain surface, an audit surface, a binary-size
cost, and an upgrade treadmill, in a codebase whose entire review posture is "we
read every line that ships".

**No decision is made here, and no dependency was added.** The decision belongs to
the founder because it trades a house rule against a plan directive. Phase 2 cannot
start until it is made; everything in Phase 1 is useful under either answer,
because a reflexive-address observation and a static peer list are equally valid
inputs to a hand-rolled ICE stack and to libp2p's AutoNAT.

## 7. Phasing

### Phase 1 — this change. REAL.

**A. STUN observability.** New `connector/internal/stun`: hand-rolled RFC 5389
binding client, zero dependencies. 20-byte header, magic cookie `0x2112A442`,
96-bit `crypto/rand` transaction ID, `XOR-MAPPED-ADDRESS` (0x0020) for IPv4 and
IPv6, legacy `MAPPED-ADDRESS` (0x0001) accepted as fallback and reported as
legacy. Security properties: the transaction ID is verified **before** any
attribute is trusted (an unmatched reply is counted as a possible off-path spoof
and discarded); every exchange is context-bounded with a deadline; reads are
bounded to 1280 bytes; attribute walking is bounds-checked against the declared
length; `ALTERNATE-SERVER` and every other redirect is **ignored, never followed**.
Two distinct servers are queried **from one local UDP socket** — a single socket is
the correctness condition, since NAT mappings are keyed on the local port and two
sockets would report "symmetric" on every NAT in existence. Same address from both
⇒ endpoint-independent; different ⇒ endpoint-dependent; fewer than two answers ⇒
**UNKNOWN**. Default servers `stun.cloudflare.com:3478` and
`turn.cloudflare.com:3478`, configurable. The transport is an injectable interface,
and the whole test suite runs **offline** — fixtures plus a loopback UDP responder
in-process; no test contacts a real STUN server.

**B. Static cross-VLAN peers.** `config.StaticPeers` (`staticPeers` in the config
file, `omitempty`, absent means none, so a config written before this change loads
unchanged). Each entry carries the **endpoint and the expected device
fingerprint** — the fingerprint is mandatory because `pool.PeerOptions` requires
`ExpectedDeviceID` and "AllowedPeers is an exact device-fingerprint allowlist, not
'trust the LAN'" (`peer.go:28`); an owner typing an IP must not weaken that.
Endpoints are validated by the **same private-IP rule** (`config.ValidPeerEndpoint`
mirrors `pool.ValidPeerEndpoint`; they cannot be the same function because
`internal/pool`'s tests import `internal/discovery` → `internal/client` →
`internal/config`, so importing `pool` from `config` is an import cycle — a test,
`TestStaticPeerEndpointRuleMatchesPool`, imports `pool` from the test binary and
asserts the two agree accept-for-accept on a table of 21 endpoints, which is a
stronger guard than a comment). Entries are merged into the existing candidate view
by `discovery.MergeStatic` and labelled `Configured` / source `static` — **clearly
statically configured, not discovered**. §30.2 still holds: configuration never
establishes membership or authorization. `AllowedPeers` still reads
`Peer.Authorized`, which only the coordinator's directory sets, so a configured
peer that the coordinator has not authorized shows as "configured, not authorized"
and is never dialled with credentials. Asserted at both layers by
`TestStaticPeerNeverEntersAllowedPeers` and
`TestAgentStaticPeerNeverEntersAllowedPeers`.

**C. Surfaces.** `nexal doctor --stun` reports the reflexive address, the NAT
classification, whether STUN was reachable, and a per-server breakdown; it is
opt-in like `--probe` because it is the only part of `doctor` that touches the
network, and it sets `networkContacted`. `doctor` also reports the static-peer
**count** (never the addresses, which are config values) with the reminder that a
configured peer is authorized only if the coordinator lists its fingerprint.
`nexal static-peers list|add|remove` manages entries; it edits the config file
under the exclusive lock and says `appliesAt: next nexal run`, rather than
pretending a live update happened. See `connector/CLI-CONTRACT.md`.

### Phase 2 — NOT STARTED.

Coordinator candidate signalling plus simultaneous-dial hole punching, and the
relay fallback for the hosts where punching cannot work. **Blocked on §6**: the
libp2p-vs-hand-roll decision determines almost every line of it, so writing any of
it now would be writing it twice. Phase 1 deliberately publishes nothing: the
reflexive address is printed locally for the owner and is never advertised to the
coordinator, because an address published without a signalling and punching design
is an address that leaks for no benefit.

## 8. Cross-VLAN operational guidance

For two Macs on separate VLANs, the transport already works; only discovery does
not. Either:

1. **Make mDNS cross the boundary.** Enable mDNS reflection/repeating on the
   switch or gateway — UniFi calls it mDNS reflector ("Multicast DNS" per-network
   toggle), Cisco calls it the Bonjour/service-discovery gateway — **plus** an
   inter-VLAN routing rule permitting the two subnets to reach each other. Then
   existing discovery works unchanged and no config edit is needed.
2. **Or skip discovery entirely** with `nexal static-peers add --endpoint
   https://10.20.0.5:8443 --fingerprint <64-hex>` on each side. This needs no
   switch feature and is the more predictable option.

In both cases the **firewall must permit the peer TCP port between the two
subnets** in the required direction(s); inter-VLAN routing is usually default-deny
on a segmented network, and a silent deny there looks exactly like a discovery
failure. Verify with `nexal doctor` (static-peer count) and then an actual peer
transfer — neither discovery nor configuration proves reachability, and neither
proves authorization.

## 9. What is real, what is not

| Claim | Status |
| --- | --- |
| Peer transport to a private IP on another VLAN | **Real, and was already permitted** (`privateIP` accepts any RFC1918 address) |
| mDNS discovery across a VLAN | **Not possible**; link-local multicast. Needs switch reflection or static peers |
| STUN reflexive address + NAT class observation | **Real**, opt-in, offline-tested, `doctor --stun` |
| Static owner-configured peers, fingerprint-pinned | **Real**, config + CLI, same private-IP rule |
| Static peer grants authorization | **No.** §30.2; coordinator's list alone sets `Authorized` |
| Hole punching / simultaneous dial | **Not built.** Phase 2 |
| Coordinator candidate signalling | **Not built.** Phase 2 |
| Any relay (Cloudflare TURN or ours) | **Rejected** for bulk bytes; §3 |
| go-libp2p | **Not added.** Conflict raised in §6, founder decides |
| Verified between two real Macs on two real VLANs | **No.** Sandbox is Linux; `GOOS=darwin go build` and `go vet` are the only checks the Darwin path has had |

One real observation exists, from a single manual `nexal doctor --stun` run in the
Linux build sandbox (not from a test): both Cloudflare servers answered, the
reflexive address was `54.237.68.156` with **different ports per server** (45039 vs
58837), so the sandbox was classified `endpoint-dependent` and the report correctly
warned that hole punching from such a host would need a relay. That is one host on
one network and says nothing about any customer's NAT — it is evidence the decoder
and the classifier work end to end against a real server, nothing more.
