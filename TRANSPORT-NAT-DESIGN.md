# Peer transport and NAT traversal: decision record

> **Superseded on 2026-09-23.** This document is retained as historical design
> context only. The libp2p/DCUtR/Circuit Relay overlay described below was never
> wired into the connector runtime and has been removed. The production network
> is the managed neXal mesh backed by NetBird/WireGuard with Rosenpass required on
> every startup and reconnect. Peer payloads, including MCP requests and results,
> travel directly to the destination's private mesh address; Cloudflare is the
> enrollment, metadata, signed-grant, policy, revocation and audit control plane,
> not a payload proxy. Path evidence is reported by the privileged mesh adapter as
> `direct_mesh` or `relay_mesh`, and a tunnel is not presented as quantum-safe
> without fresh runtime Rosenpass evidence. The STUN and static-peer material in
> this record remains useful diagnostic/history, but does not define production
> routing.

Scope: `connector/` only. This records what the peer transport does **today**, what
changed in Phase 1 and Phase 2, what is deliberately **not** built, and how the one
conflict with the platform hardening plan was resolved.

**The conflict is resolved.** §16 line 872 selects go-libp2p; the house rule was
zero dependencies. The founder chose **go-libp2p on 2026-09-21**, an explicit
override of the zero-dep rule on the plan's authority. §6 records it, and the scope
of the override is exact: `connector/internal/p2p` and nothing else. Do not revert
it as an accident.

**Phase 1 (STUN observability, static peers) established no peer-to-peer connection
over the internet — read §5.** Phase 2 builds the path that can, and it is
**feature-flagged off by default** (§7): `config.p2p` is absent in every config
written before this change, absent means `enabled:false`, and a disabled host builds
no libp2p host at all. Read §9 for the line-by-line real-vs-not, §10 for what a
relay operator can see, §11 for the coordinator work still required, and §13 for why
the existing LAN transport is untouched and still preferred.

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

## 6. CONFLICT RESOLVED 2026-09-21: go-libp2p, by founder override

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

### DECIDED 2026-09-21: go-libp2p, founder override of the zero-dep rule, on the plan's authority

The conflict above was put to the founder as stated — HARDENING-PLAN line 872
selects go-libp2p, the standing house rule is hand-roll and keep zero
dependencies — and he chose **go-libp2p, as the plan says**. This is an explicit
founder override of the zero-dependency rule, made on the authority of §16, not an
oversight and not a drift.

**This is recorded so nobody reverts it as an accident.** A future contributor
reading `connector/go.mod` will find a dependency tree in a repo whose other
packages are standard-library-only, and the natural reading of that is "somebody
slipped a dep in". It is not. The scope of the override is exact:

- go-libp2p is permitted **in `connector/internal/p2p` only**. Every other package
  in `connector/` still compiles against the standard library alone, and that is
  the property to preserve.
- The override covers **this dependency and its transitive closure**, nothing
  else. It is not a general relaxation. No unrelated dependency may be added
  anywhere in the tree on the strength of this decision.
- `internal/p2p/p2p.go`'s package comment repeats the decision and its date at the
  point of use, because a decision recorded only in a design document is a decision
  the next person will not find.

Everything in Phase 1 survived the decision unchanged, as predicted: the reflexive
address observation became AutoNAT's cheap pre-check (§10) rather than being
thrown away, and the static peer list is untouched.

## 7. Phasing

### Phase 1 — REAL (shipped earlier).

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

### Phase 2 — this change. REAL, and OFF BY DEFAULT.

New package `connector/internal/p2p`, built on go-libp2p per §6. It delivers the
three pieces HARDENING-PLAN §16 names. **The whole path is feature-flagged off**:
`config.p2p` is absent in every config written before this change, absent means
`enabled:false`, and a disabled host builds no libp2p host at all — no socket, no
goroutines, no published address, no reservation. Shipping this cannot regress an
existing user because for an existing user nothing runs
(`TestDisabledHostIsInert`).

**A. AutoNAT — dialability, reconciled with `internal/stun` rather than duplicating
it.** `internal/p2p/nat.go`. The two signals answer **different questions** and
neither supersedes the other, which is why there is exactly one type
(`Dialability`) that consumers read and no way to read a punchability conclusion
out of AutoNAT or a dialability conclusion out of STUN:

| | question | predicts | authority |
| --- | --- | --- | --- |
| `internal/stun` (Phase 1) | does my NAT reuse one mapping across destinations? | whether **hole punching** can work | the cheap pre-check, and the **only** signal available with the flag off |
| libp2p AutoNAT | can a peer complete an inbound dial to an address I advertised? | whether a **relay** is needed | authoritative on **dialability** — an end-to-end test by a third party, not an inference |

`Reconcile` composes them. `private` + `endpoint-dependent` is `agree` (both
independently say "relay required"). `private` + `endpoint-independent` is
`complementary`, **not** a conflict — it is the ordinary home-network state and
printing it as a contradiction would train owners to ignore the field. A measured
`endpoint-dependent` NAT skips the punch entirely, because §16 says symmetric and
carrier-grade NAT defeat it. Everything else attempts the punch first, since a
punch is cheap and the relay is the expensive path. Phase 1's honesty rule still
holds and is still asserted by a test: no summary claims hole punching *will*
work.

**B. DCUtR — address exchange over the relay, RTT, simultaneous dial.**
`libp2p.EnableHolePunching`, with a tracer (`internal/p2p/punch.go`) because libp2p
punches **silently** otherwise, and §5's complaint about Phase 1 was precisely that
an unobservable transport property is one you cannot decide anything about. The
surfaced RTT is DCUtR's own CONNECT round trip — the value the synchronised dial
was timed against — not a ping.

**C. Circuit Relay v2 — fallback with ceilings on all three axes.**
`internal/p2p/limits.go`. HARDENING-PLAN lines 877-879 make the byte ceiling the
cost control: "a fallback path cannot silently run up spend". An unbounded relay
path is therefore a **failed** implementation, not an incomplete one. Three axes,
three enforcement points, because they are knowable at three different moments:

| axis | as relay **service** (we carry others' bytes) | as relay **client** (our bytes) |
| --- | --- | --- |
| connections | `relayv2.Resources.MaxCircuits` / `MaxReservations`, inside libp2p | `InterceptAddrDial` + `InterceptAccept`, via `Ledger` |
| time | `relayv2.RelayLimit.Duration` resets the circuit | per-circuit deadline, reaped by a 5 s watchdog ticker |
| bytes | `relayv2.RelayLimit.Data` resets the circuit | `MeteredStream` charges **every read and write** |

The client-side column is ours because libp2p imposes no ceiling on bytes we push
through someone **else's** relay; relying on a counterparty to enforce our budget
would mean we cannot state a bound. All ceilings are owner-configurable under
`config.p2p`, a **zero means the reviewed default and never unlimited** (no value
expresses unlimited, on purpose), and hard outer bounds stop a fat-fingered config
becoming an uncapped relay. Consumption is surfaced with each limit beside its
usage, and refusals are counted separately from truncations — "we protected you"
and "a transfer failed, here is why" mean different things to an owner.

Two honest limitations, stated on the surface itself rather than in this document
only (`Limits.Stated()`): the byte counters cover data streams on the nexal
protocol and not libp2p's own control chatter, and the total **resets on process
restart**, so it is *not* the per-donor **monthly** budget §16 also requires. That
one needs persistence and a calendar the connector does not have; claiming a
monthly cap that silently resets would be worse than claiming a process-lifetime
cap.

**D. Direct connections are never metered.** Metering the good path would throttle
the transport the relay ceiling exists to protect. Only relayed streams are
wrapped, asserted in both directions by test.

### Phase 2 — what is still NOT built

- **Coordinator signalling endpoints.** Out of scope by instruction. The seam is
  `p2p.Rendezvous`, the in-process `StubRendezvous` satisfies the tests, and §11
  lists the four endpoints the coordinator must expose. With no rendezvous a host
  can accept and can relay, but cannot initiate to a peer whose address it does not
  already hold — `ErrNoRendezvous`, a distinct error so "not wired up" cannot be
  mistaken for "unreachable".
- **Any agent/CLI wiring.** `internal/p2p` is complete and tested but nothing
  constructs it yet; `nexal doctor` does not print `Snapshot` yet.
- **A proven hole punch.** It cannot be proven offline — see §9.
- **The §16 monthly per-donor bandwidth budget and cap-aware scheduling.**
- **ML-DSA.** See §12.

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
| go-libp2p | **Added, `internal/p2p` only.** Founder override 2026-09-21; §6 |
| libp2p path active for existing users | **No.** `config.p2p` absent ⇒ `enabled:false`; a disabled host builds no libp2p host at all |
| AutoNAT dialability, reconciled with `internal/stun` | **Real**, `p2p.Dialability`; the two signals compose, neither supersedes (§7 Phase 2 A) |
| DCUtR hole punching wired + RTT/outcome surfaced | **Real** (`EnableHolePunching` + tracer) |
| A hole punch **proven** to traverse two real NATs | **No, and not provable here.** Loopback has no public address, so libp2p never even registers the DCUtR handler in a sandbox. Only two real NATed hosts can prove this |
| Circuit Relay v2 fallback | **Real**, client and (opt-in) service |
| Relay ceilings on connections, time **and** bytes | **Real and enforced**, owner-configurable, zero means default and never unlimited; consumption surfaced. Lines 877-879 |
| Monthly per-donor bandwidth budget (§16) | **Not built.** The byte total resets on restart, and every surface says so |
| Relay sees customer file content | **No.** End-to-end libp2p security through the circuit. It *does* see both fingerprints, IPs, volume and timing — §10 |
| Coordinator candidate signalling | **Not built, out of scope.** Seam is `p2p.Rendezvous`; §11 lists the four endpoints |
| Agent/CLI wiring for the libp2p path | **Not built.** `internal/p2p` is complete and tested but nothing constructs it yet |
| libp2p can admit a peer the coordinator never authorized | **No.** Gate at `InterceptSecured` (before the muxer, so no stream can open), re-checked at stream open; the transport has no path that ADDS to the allowlist |
| `privateIP` / LAN mutual-TLS path changed | **No.** Untouched and preferred when a private address exists; `TestLANRuleIsNotWeakened` imports `pool` and asserts the rule accept-for-accept |
| Any relay (Cloudflare TURN or ours) for bulk bytes | **Still rejected**; §3. The relay here is a donor's, per §16 |
| Verified between two real Macs on two real VLANs | **No.** Sandbox is Linux; `GOOS=darwin go build` and `go vet` are the only checks the Darwin path has had |

One real observation exists, from a single manual `nexal doctor --stun` run in the
Linux build sandbox (not from a test): both Cloudflare servers answered, the
reflexive address was `54.237.68.156` with **different ports per server** (45039 vs
58837), so the sandbox was classified `endpoint-dependent` and the report correctly
warned that hole punching from such a host would need a relay. That is one host on
one network and says nothing about any customer's NAT — it is evidence the decoder
and the classifier work end to end against a real server, nothing more.

## 10. What the relay operator can and cannot see

Stated plainly, because overstating this is worse than admitting a gap. A Circuit
Relay v2 hop carries an opaque byte stream; the two endpoints run their **own**
libp2p security handshake (TLS 1.3 or Noise) end to end **through** the circuit,
and the relay is not a party to it.

**The relay CANNOT see:**

- **Customer file content.** Payload bytes are encrypted end to end between the two
  connectors. The relay forwards ciphertext.
- **Which application protocol is in use, or stream framing** — protocol
  negotiation happens inside the encrypted session.
- **Anything it could substitute itself into.** Both peer IDs are authenticated
  end to end, so a relay that tried to terminate the session itself fails the
  handshake rather than becoming a silent man in the middle.

**The relay CAN see, and this is not a small list:**

- **Both peers' identities.** It is told the destination peer ID in the `CONNECT`
  message and knows the reserving peer's ID from the reservation. Since our peer
  IDs embed their public keys, the relay can derive both **device fingerprints**.
- **The reserving peer's IP addresses**, and the dialling peer's source IP.
- **Byte volume, direction, timing and duration** of every circuit it carries —
  i.e. enough for traffic analysis: who backs up to whom, how much, and when.
- **Nothing about membership**, and it gains nothing by carrying traffic: a relay
  is not an authorizer (§30.2), and the relay itself must be an authorized peer
  before we will reserve on it, precisely because of the metadata above.

The confidentiality claim therefore rests on **libp2p's security transport**, not
on the `internal/pool` mutual-TLS layer, which does not run over this path. That is
a different trust surface from the LAN path and is called out here rather than
blurred: on the LAN path, confidentiality and authorization are the same
handshake; on the libp2p path they are two mechanisms — libp2p's transport
security for confidentiality, and the coordinator's fingerprint allowlist,
re-checked at connect **and** at stream open, for authorization.

## 11. What the coordinator must expose (NOT built — out of scope)

Four additive endpoints. None may carry authorization: the peer directory's
`Authorized` flag remains the only thing that admits a peer, and each endpoint must
be refused for a caller whose own device is not authorized.

1. `POST /v1/peers/self/p2p` — publish this device's libp2p peer ID, advertised
   multiaddrs, and relay-reservation addresses. This is where an address leaves the
   machine for the first time (Phase 1 published nothing), so it must be gated on
   the owner's flag and must never include an address the host has not bound.
2. `GET /v1/peers/{deviceId}/p2p` — one authorized peer's peer ID and multiaddrs.
   **404 for a device the caller may not transfer with**, so the endpoint cannot be
   used to enumerate membership.
3. `GET /v1/relays` — relay candidates: authorized, publicly dialable donor devices
   that opted into relay duty. §16 requires donors, paid, never company-run
   infrastructure — so this list is the coordinator's, not a bootstrap list
   hardcoded in the binary.
4. `DELETE /v1/peers/self/p2p` — withdraw on shutdown or when the owner disables
   the flag. Without it, peers keep being handed a stale address that fails to
   dial.

**One property the connector cannot enforce for other peers and the coordinator
must:** a published peer ID has to derive to the DeviceID it was published under.
The check is `DeviceIDFromPeerID(peerID) == deviceId`, four lines. Without it a
member could publish someone else's peer ID and redirect their traffic. The
connector **re-checks it on every fetch** anyway (`TestCoordinatorRecordPairing
IsRechecked`), because a client that assumes the server checked is a client with a
second source of truth about identity — but the coordinator should reject it at
write time too.

## 12. ML-DSA (§2) migration implication — written down, not implemented

§16 warns the §2 post-quantum migration "has to be planned across both at once".
Concretely, and the detail is in `internal/p2p/identity.go`:

- Today one identity has two encodings. `DeviceID = sha256(ed25519 pubkey)`
  (`identity.go:33-39`), and an Ed25519 libp2p peer ID is the **identity**
  multihash of the marshalled public key — the key rides *inside* the peer ID. So
  `peer.ID → pubkey → DeviceID` is total, deterministic and needs no directory,
  which is what makes the authorization gate possible at all.
- ML-DSA breaks **both halves at once**. `DeviceID` becomes
  `sha256(ML-DSA pubkey)`, changing every fingerprint in every coordinator record,
  every `staticPeers` entry and every operator's notes. And libp2p has **no ML-DSA
  key type** — its key enum is RSA, Ed25519, Secp256k1, ECDSA — while an ML-DSA-44
  public key (≈1312 bytes) is far too large to sit in an identity multihash the way
  a 32-byte Ed25519 key does, so peer IDs would become hash-based and
  `ExtractPublicKey` would stop working, removing the directory-free derivation.
- **Therefore the order is forced.** The coordinator must serve *both* fingerprints
  for a peer through a dual-fingerprint window before any connector switches, and
  the libp2p side needs either a hybrid handshake (Ed25519 peer identity carrying an
  ML-DSA-signed binding to the new DeviceID) or a libp2p release with an ML-DSA key
  type. Until one exists, **§2 cannot land for the libp2p path even if it lands for
  the LAN path** — and shipping it for one transport only would create exactly the
  two-sources-of-truth-about-identity problem this design otherwise avoids.

## 13. Two transports coexist; the LAN path is preferred and untouched

This is the invariant most at risk from a change this size, so it is stated last
and tested twice.

`internal/pool`'s mutual-TLS HTTPS path to a literal private IP is the **proven**
transport. Phase 2 changed **not one line** of it. `privateIP`
(`peer.go:314-317`) and `ValidPeerEndpoint` are byte-identical, and
`TestLANRuleIsNotWeakened` imports `internal/pool` from the test binary and asserts
the rule still refuses every public address and still accepts every RFC1918,
loopback and link-local one — the same technique `TestStaticPeerEndpointRuleMatches
Pool` used in Phase 1, because the guard against a loosened rule is a test, not a
comment.

libp2p is an **additional** path for peers no private address can reach. The
routing rule:

1. If the peer has a usable private address → **LAN mutual-TLS path**. `p2p.Connect`
   refuses (`ErrInvalid`) when every address it was given is private, and counts it
   as `lanPreferred` in the snapshot. A high count there is good news, not an error
   rate (`TestLANIsPreferredOverLibp2p`).
2. Otherwise, libp2p: direct dial, then DCUtR, then a relayed circuit under the
   ceilings.

`p2p.PrivateAddr` is the routing hint for step 1. It deliberately **mirrors** the
shape of `privateIP` without importing it, and a test asserts the two agree
case-for-case — so it can never quietly become a looser second copy of the security
rule. The authoritative rule for what the LAN transport will dial remains
`pool.ValidPeerEndpoint`.

Authorization is identical across both transports and comes from one place: the
coordinator's `Authorized` set. On the LAN path the mutual-TLS `peerPolicy` pins an
exact fingerprint; on the libp2p path the connection gater derives the fingerprint
from the authenticated peer ID and checks the same allowlist, at
`InterceptSecured` — before the muxer is negotiated, so an unauthorized peer cannot
open a stream at all — and again at stream open, so a long-lived connection cannot
outlive a revocation. An empty allowlist denies everything, matching
`pool.newPeerPolicy`'s refusal of `len(allowed) < 1`. **No code path in
`internal/p2p` can add to the allowlist**, which is the property that keeps libp2p
from becoming a §30.2 back door, and it has its own test.

There is **no DHT, no mDNS, and no public bootstrap list** in the libp2p wiring.
That is a security decision, not minimalism: a global DHT would publish members'
addresses to strangers and would make open discovery a thing this host does, and
§30.2 forbids discovery from carrying authorization weight anyway. Peers come from
the coordinator or they do not come.
